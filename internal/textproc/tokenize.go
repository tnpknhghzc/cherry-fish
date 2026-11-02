package textproc

import (
	"strings"
	"unicode"
)

var stop = func() map[string]struct{} {
	m := map[string]struct{}{}
	for _, w := range strings.Fields(`и в во не что он на я с со как а то все она так его но да ты к у же вы за бы по только ее мне было вот от меня еще нет о из ему теперь когда даже ну вдруг ли если уже или ни быть был него до вас нибудь опять уж вам сказал ведь там потом себя ничего ей может они тут где есть надо ней для мы тебя их чем была сам чтоб без будто чего раз тоже себе под будет ж тогда кто этот того потому этого какой совсем ним здесь этом один почти мой тем чтобы нее сейчас были куда зачем всех никогда можно при наконец два об другой хоть после над больше тот через эти нас про всего них какая много разве три эту моя впрочем хорошо свою этой перед иногда лучше чуть том нельзя такой им более всегда конечно всю между
the a an and or of to in on at for with by from is are was were be been this that these those it its as not no but if then than so such into about over under what which who whom how why when where
расскажи скажи объясни кратко подробно главе книге книги тексте документе согласно автор автора`) {
		m[w] = struct{}{}
	}
	return m
}()

// Normalize: нижний регистр, ё→е.
func Normalize(s string) string {
	s = strings.ToLower(s)
	return strings.ReplaceAll(s, "ё", "е")
}

// Tokens разбивает текст на нормализованные стеммированные токены для BM25.
func Tokens(text string) []string {
	text = Normalize(text)
	var out []string
	var cur []rune
	flush := func() {
		if len(cur) == 0 {
			return
		}
		rs := append([]rune(nil), cur...)
		cur = cur[:0]
		if len(rs) < 2 {
			return
		}
		w := string(rs)
		if _, bad := stop[w]; bad {
			return
		}
		if isCyrillic(rs[0]) {
			w = StemRU(w)
		} else if rs[0] >= 'a' && rs[0] <= 'z' {
			w = StemEN(w)
		}
		out = append(out, w)
	}
	for _, r := range text {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			cur = append(cur, r)
		} else {
			flush()
		}
	}
	flush()
	return out
}
