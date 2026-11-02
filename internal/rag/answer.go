package rag

import (
	"context"
	"fmt"
	"strings"
	"unicode"

	"github.com/tnpknhghzc/locallm/internal/llm"
	"github.com/tnpknhghzc/locallm/internal/store"
	"github.com/tnpknhghzc/locallm/internal/textproc"
)

type Answerer struct {
	R           *Retriever
	LLM         llm.LLM
	Title       string
	MaxContext  int // потолок токенов под найденные фрагменты (меньше → быстрее на CPU)
	MaxAnswer   int
	Temperature float32
}

const NoInfoRU = "В загруженном документе я не нашёл информации для ответа на этот вопрос."

func isRussian(s string) bool {
	cyr, lat := 0, 0
	for _, r := range s {
		switch {
		case unicode.Is(unicode.Cyrillic, r):
			cyr++
		case unicode.Is(unicode.Latin, r):
			lat++
		}
	}
	return cyr >= lat
}

func systemPrompt(title string, ru bool) string {
	if ru {
		return fmt.Sprintf("Ты — помощник для работы с документом «%s». Отвечай ТОЛЬКО на основе приведённых фрагментов. "+
			"Если в них нет ответа, прямо скажи об этом и ничего не выдумывай. Отвечай на языке вопроса, чётко и по делу. "+
			"Ссылайся на источники номерами в квадратных скобках, например [1], [2].", title)
	}
	return fmt.Sprintf("You are an assistant for the document \"%s\". Answer ONLY from the provided excerpts. "+
		"If they do not contain the answer, say so plainly and do not invent anything. Answer in the language of the question. "+
		"Cite sources with bracketed numbers such as [1], [2].", title)
}

func (a *Answerer) count(s string) int { return a.LLM.CountTokens(s) }

func trimRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// Answer отвечает на вопрос по книге с учётом истории чата.
func (a *Answerer) Answer(ctx context.Context, question string, history []store.Message, onToken func(string)) (string, []store.Source, error) {
	if a.MaxContext == 0 {
		a.MaxContext = 2200
	}
	if a.MaxAnswer == 0 {
		a.MaxAnswer = 600
	}
	// запрос для поиска: короткие уточнения дополняем предыдущим вопросом
	query := question
	if len(textproc.Tokens(question)) <= 3 {
		for i := len(history) - 1; i >= 0; i-- {
			if history[i].Role == "user" {
				query = history[i].Content + " " + question
				break
			}
		}
	}
	ru := isRussian(question)
	sys := systemPrompt(a.Title, ru)

	// история: последние 2 обмена, ответы урезаем
	var hist []llm.Message
	start := max(0, len(history)-4)
	histTokens := 0
	for _, m := range history[start:] {
		c := m.Content
		if m.Role == "assistant" {
			c = trimRunes(c, 400)
		} else {
			c = trimRunes(c, 300)
		}
		histTokens += a.count(c) + 8
		hist = append(hist, llm.Message{Role: m.Role, Content: c})
	}
	overhead := a.count(sys) + a.count(question) + histTokens + 80
	budget := a.LLM.ContextSize() - a.MaxAnswer - overhead
	budget = min(budget, a.MaxContext)
	if budget < 300 {
		hist, histTokens = nil, 0
		budget = min(a.LLM.ContextSize()-a.MaxAnswer-a.count(sys)-a.count(question)-80, a.MaxContext)
	}
	passages, err := a.R.Search(ctx, query, SearchOptions{K: 10, TokenBudget: budget, Count: a.count})
	if err != nil {
		return "", nil, err
	}
	if len(passages) == 0 {
		msg := NoInfoRU
		if !ru {
			msg = "I could not find relevant information in the document."
		}
		if onToken != nil {
			onToken(msg)
		}
		return msg, nil, nil
	}
	label, qlabel := "Фрагменты документа:", "Вопрос:"
	if !ru {
		label, qlabel = "Document excerpts:", "Question:"
	}
	build := func(ps []Passage) []llm.Message {
		var ctxText strings.Builder
		for i, p := range ps {
			pages := fmt.Sprintf("стр. %d", p.PageFrom)
			if p.PageTo > p.PageFrom {
				pages = fmt.Sprintf("стр. %d–%d", p.PageFrom, p.PageTo)
			}
			if !ru {
				pages = strings.Replace(pages, "стр.", "p.", 1)
			}
			fmt.Fprintf(&ctxText, "[%d] (%s; %s)\n%s\n\n", i+1, pages, p.Section, strings.TrimSpace(p.Text))
		}
		msgs := []llm.Message{{Role: "system", Content: sys}}
		msgs = append(msgs, hist...)
		return append(msgs, llm.Message{Role: "user", Content: label + "\n\n" + ctxText.String() + qlabel + " " + question})
	}
	total := func(msgs []llm.Message) int { // + запас на служебные токены шаблона чата
		n := 16
		for _, m := range msgs {
			n += a.count(m.Content) + 10
		}
		return n
	}
	// Подгоняем промпт под окно контекста по РЕАЛЬНОМУ числу токенов: сначала выбрасываем
	// наименее релевантные фрагменты, затем историю, затем укорачиваем ответ/фрагмент.
	maxAns := a.MaxAnswer
	limit := a.LLM.ContextSize() - 24
	msgs := build(passages)
	for total(msgs)+maxAns > limit {
		switch {
		case len(passages) > 1:
			worst := 0
			for i, p := range passages {
				if p.Score < passages[worst].Score {
					worst = i
				}
			}
			passages = append(passages[:worst:worst], passages[worst+1:]...)
		case len(hist) > 0:
			hist = nil
		case maxAns > 160:
			maxAns = max(160, maxAns/2)
		default:
			r := []rune(passages[0].Text)
			if len(r) < 200 {
				return "", nil, fmt.Errorf("окно контекста модели (%d токенов) слишком мало", a.LLM.ContextSize())
			}
			passages[0].Text = string(r[:len(r)*7/10])
		}
		msgs = build(passages)
	}
	var sources []store.Source
	for _, p := range passages {
		sources = append(sources, store.Source{Chunk: p.Chunks[0], Section: p.Section, PageFrom: p.PageFrom, PageTo: p.PageTo, Snippet: trimRunes(p.Text, 220)})
	}
	ans, err := a.LLM.Chat(ctx, msgs, llm.Options{MaxTokens: maxAns, Temperature: a.tmp()}, onToken)
	return ans, sources, err
}

func (a *Answerer) tmp() float32 {
	if a.Temperature == 0 {
		return 0.2
	}
	return a.Temperature
}

// SummarizeSection — краткое содержание раздела по равномерной выборке фрагментов.
func (a *Answerer) SummarizeSection(ctx context.Context, sid int, onToken func(string)) (string, error) {
	t := a.R.Tree
	if sid < 0 || sid >= len(t.Sections) {
		return "", fmt.Errorf("нет раздела %d", sid)
	}
	var ids []int
	for _, sub := range a.R.subtree[sid] {
		s := t.Sections[sub]
		for c := s.ChunkFrom; c < s.ChunkTo; c++ {
			ids = append(ids, c)
		}
	}
	if len(ids) == 0 {
		return "", fmt.Errorf("в разделе нет текста")
	}
	if a.MaxContext == 0 {
		a.MaxContext = 2200
	}
	if a.MaxAnswer == 0 {
		a.MaxAnswer = 600
	}
	budget := min(a.MaxContext, a.LLM.ContextSize()-a.MaxAnswer-250)
	// шаг выборки подбираем так, чтобы фрагменты уместились в бюджет
	avg := a.count(t.Chunks[ids[0]].Text) + 8
	n := max(1, min(len(ids), budget/avg))
	var sb strings.Builder
	for i := 0; i < n; i++ {
		c := t.Chunks[ids[i*len(ids)/n]]
		fmt.Fprintf(&sb, "(стр. %d) %s\n\n", c.PageFrom, strings.TrimSpace(c.Text))
	}
	msgs := []llm.Message{
		{Role: "system", Content: "Ты кратко и точно пересказываешь содержание по выдержкам. Не выдумывай ничего, чего нет в выдержках. Пиши на языке выдержек."},
		{Role: "user", Content: fmt.Sprintf("Раздел: «%s». Выдержки (равномерно по разделу):\n\n%sСделай краткое содержание раздела: основные темы и выводы, 5–8 предложений.", t.Sections[sid].Title, sb.String())},
	}
	return a.LLM.Chat(ctx, msgs, llm.Options{MaxTokens: 450, Temperature: 0.2}, onToken)
}

// SummarizeBook — обзор книги по заголовкам верхнего уровня и уже готовым сводкам разделов.
func (a *Answerer) SummarizeBook(ctx context.Context, cached map[string]string, onToken func(string)) (string, error) {
	t := a.R.Tree
	budget := min(a.MaxContextOr(), a.LLM.ContextSize()-700)
	var parts []string
	for _, s := range t.Sections {
		if s.Parent != -1 {
			continue
		}
		line := "• " + s.Title
		if sum, ok := cached[fmt.Sprint(s.ID)]; ok {
			line += ": " + trimRunes(sum, 500)
		} else if s.ChunkTo > s.ChunkFrom {
			line += ": " + trimRunes(t.Chunks[s.ChunkFrom].Text, 250)
		}
		parts = append(parts, line)
	}
	// при слишком большом числе разделов прореживаем равномерно
	for len(parts) > 1 && a.count(strings.Join(parts, "\n")) > budget {
		var np []string
		for i, p := range parts {
			if i%2 == 0 {
				np = append(np, p)
			}
		}
		parts = np
	}
	msgs := []llm.Message{
		{Role: "system", Content: "Ты кратко и точно описываешь книгу по оглавлению и выдержкам. Не выдумывай того, чего нет в данных. Пиши на языке данных."},
		{Role: "user", Content: fmt.Sprintf("Книга «%s». Оглавление и выдержки:\n\n%s\n\nСделай обзор книги: о чём она, как устроена, главные идеи (6–10 предложений).", a.Title, strings.Join(parts, "\n"))},
	}
	return a.LLM.Chat(ctx, msgs, llm.Options{MaxTokens: 600, Temperature: 0.2}, onToken)
}

func (a *Answerer) MaxContextOr() int {
	if a.MaxContext == 0 {
		return 2200
	}
	return a.MaxContext
}
