// Команда locallm — единый исполняемый файл: чат с книгой в терминале,
// локальный API для UI, упаковка моделей внутрь бинарника.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"html"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tnpknhghzc/locallm/internal/extract"
	"github.com/tnpknhghzc/locallm/internal/hw"
	"github.com/tnpknhghzc/locallm/internal/payload"
	"github.com/tnpknhghzc/locallm/internal/server"
	"github.com/tnpknhghzc/locallm/pkg/locallm"
)

var version = "dev"

const usage = `locallm — локальный ИИ для работы с большими книгами (без интернета)

Использование:
  locallm                        открыть графический интерфейс (доски, чат, оглавление) в браузере
  locallm ui                     то же самое
  locallm <файл>                 открыть книгу и начать диалог (PDF, DOCX, ODT, EPUB, FB2, TXT, MD, HTML, RTF)
  locallm chat <файл|id>         то же самое
  locallm ask <файл> "вопрос"    один вопрос и выход
  locallm index <файл>           только проиндексировать
  locallm search <файл|id> "запрос"  найти места в книге без генерации ответа (мгновенно)
  locallm books                  список проиндексированных книг
  locallm toc <id>               оглавление книги
  locallm summary <id> [раздел]  краткое содержание раздела (без номера — всей книги)
  locallm serve [-addr ...]      локальный API для интерфейса (127.0.0.1)
  locallm pack -o out [модели]   вшить модели .gguf в один исполняемый файл
  locallm doctor                 диагностика: железо, модели, проверка работы
  locallm version

Общие флаги: -data <папка> -models <папка> -profile fast|balanced|quality -threads N -ctx N`

type common struct {
	data, models, profile string
	threads, ctx          int
}

func (c *common) bind(fs *flag.FlagSet) {
	fs.StringVar(&c.data, "data", "", "папка данных (индексы, чаты)")
	fs.StringVar(&c.models, "models", "", "папка с .gguf моделями")
	fs.StringVar(&c.profile, "profile", "balanced", "fast | balanced | quality")
	fs.IntVar(&c.threads, "threads", 0, "потоки CPU (0 — авто)")
	fs.IntVar(&c.ctx, "ctx", 0, "окно контекста (0 — авто)")
}

func (c *common) open() (*locallm.Engine, error) {
	return locallm.Open(locallm.Options{DataDir: c.data, ModelsDir: c.models, Profile: c.profile, Threads: c.threads, ContextSize: c.ctx,
		Log: func(s string) { fmt.Fprintln(os.Stderr, s) }})
}

func main() {
	args := os.Args[1:]
	if len(args) == 0 {
		if err := cmdServe([]string{"-open"}); err != nil {
			fmt.Fprintln(os.Stderr, "Ошибка:", err)
			if runtime.GOOS == "windows" { // окно закрылось бы мгновенно при двойном щелчке
				fmt.Print("\nНажмите Enter для выхода...")
				bufio.NewReader(os.Stdin).ReadString('\n')
			}
			os.Exit(1)
		}
		return
	}
	cmd, rest := args[0], args[1:]
	var err error
	switch cmd {
	case "help", "-h", "--help":
		fmt.Println(usage)
	case "version", "--version":
		fmt.Println("locallm", version, runtime.GOOS+"/"+runtime.GOARCH)
	case "chat":
		err = cmdChat(rest)
	case "ask":
		err = cmdAsk(rest)
	case "index":
		err = cmdIndex(rest)
	case "search":
		err = cmdSearch(rest)
	case "books":
		err = cmdBooks(rest)
	case "toc":
		err = cmdTOC(rest)
	case "summary":
		err = cmdSummary(rest)
	case "serve":
		err = cmdServe(rest)
	case "ui":
		err = cmdServe(append([]string{"-open"}, rest...))
	case "pack":
		err = cmdPack(rest)
	case "doctor":
		err = cmdDoctor(rest)
	default:
		if _, e := os.Stat(cmd); e == nil || strings.HasPrefix(cmd, "-") && len(args) > 1 {
			err = cmdChat(args)
		} else {
			fmt.Fprintln(os.Stderr, "неизвестная команда:", cmd)
			fmt.Println(usage)
			os.Exit(2)
		}
	}
	if err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintln(os.Stderr, "Ошибка:", err)
		os.Exit(1)
	}
}

func bar(p locallm.Progress) {
	switch p.Stage {
	case "extract":
		if p.Total > 0 {
			fmt.Fprintf(os.Stderr, "\rЧтение документа: %d/%d стр.   ", p.Done, p.Total)
		}
	case "embed":
		fmt.Fprintf(os.Stderr, "\rИндексация: %d/%d фрагментов (%d%%)   ", p.Done, p.Total, p.Done*100/max(p.Total, 1))
	case "ready":
		fmt.Fprintln(os.Stderr, "\rИндексация завершена.                              ")
	}
}

// openBook принимает путь к файлу или id уже проиндексированной книги.
func openBook(ctx context.Context, e *locallm.Engine, ref string, fullIndex bool) (*locallm.Book, error) {
	var b *locallm.Book
	var err error
	if _, serr := os.Stat(ref); serr == nil {
		b, err = e.AddBook(ctx, ref, bar)
	} else {
		b, err = e.OpenBook(ref)
	}
	if err != nil {
		return nil, err
	}
	if d, t := b.IndexState(); d < t && fullIndex {
		fmt.Fprintf(os.Stderr, "Книга: %s (%d стр., %d фрагментов). Строю векторный индекс (один раз).\n", b.Meta().Title, b.Meta().Pages, t)
		if err := b.Index(ctx, bar); err != nil {
			return nil, err
		}
	}
	return b, nil
}

func ctxSignal() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt)
}

func cmdIndex(args []string) error {
	fs := flag.NewFlagSet("index", flag.ExitOnError)
	var c common
	c.bind(fs)
	fs.Parse(args)
	if fs.NArg() < 1 {
		return errors.New("укажите файл")
	}
	e, err := c.open()
	if err != nil {
		return err
	}
	defer e.Close()
	ctx, stop := ctxSignal()
	defer stop()
	b, err := openBook(ctx, e, fs.Arg(0), true)
	if err != nil {
		return err
	}
	fmt.Println("Готово. id книги:", b.ID())
	return nil
}

func cmdSearch(args []string) error {
	fs := flag.NewFlagSet("search", flag.ExitOnError)
	var c common
	c.bind(fs)
	k := fs.Int("k", 5, "сколько фрагментов показать")
	fs.Parse(args)
	if fs.NArg() < 2 {
		return errors.New(`использование: locallm search <файл|id> "запрос"`)
	}
	e, err := c.open()
	if err != nil {
		return err
	}
	defer e.Close()
	ctx, stop := ctxSignal()
	defer stop()
	b, err := openBook(ctx, e, fs.Arg(0), false)
	if err != nil {
		return err
	}
	ps, err := b.Search(ctx, strings.Join(fs.Args()[1:], " "), *k)
	if err != nil {
		return err
	}
	if len(ps) == 0 {
		fmt.Println("Ничего не найдено.")
	}
	for i, p := range ps {
		pg := fmt.Sprint(p.PageFrom)
		if p.PageTo > p.PageFrom {
			pg = fmt.Sprintf("%d–%d", p.PageFrom, p.PageTo)
		}
		txt := []rune(strings.Join(strings.Fields(p.Text), " "))
		if len(txt) > 220 {
			txt = append(txt[:219], '…')
		}
		fmt.Printf("[%d] стр. %s — %s\n    %s\n", i+1, pg, p.Section, string(txt))
	}
	return nil
}

func cmdAsk(args []string) error {
	fs := flag.NewFlagSet("ask", flag.ExitOnError)
	var c common
	c.bind(fs)
	fs.Parse(args)
	if fs.NArg() < 2 {
		return errors.New(`использование: locallm ask <файл|id> "вопрос"`)
	}
	e, err := c.open()
	if err != nil {
		return err
	}
	defer e.Close()
	ctx, stop := ctxSignal()
	defer stop()
	b, err := openBook(ctx, e, fs.Arg(0), true)
	if err != nil {
		return err
	}
	return ask(ctx, b, "default", strings.Join(fs.Args()[1:], " "))
}

func ask(ctx context.Context, b *locallm.Book, chat, q string) error {
	ans, err := b.Ask(ctx, locallm.AskRequest{ChatID: chat, Question: q}, func(t string) { fmt.Print(t) })
	fmt.Println()
	if err != nil {
		return err
	}
	if len(ans.Sources) > 0 {
		fmt.Println("\nИсточники:")
		for i, s := range ans.Sources {
			pg := fmt.Sprint(s.PageFrom)
			if s.PageTo > s.PageFrom {
				pg = fmt.Sprintf("%d–%d", s.PageFrom, s.PageTo)
			}
			fmt.Printf("  [%d] стр. %s — %s\n", i+1, pg, s.Section)
		}
	}
	return nil
}

func cmdChat(args []string) error {
	fs := flag.NewFlagSet("chat", flag.ExitOnError)
	var c common
	c.bind(fs)
	fs.Parse(args)
	if fs.NArg() < 1 {
		return errors.New("укажите файл книги или её id")
	}
	e, err := c.open()
	if err != nil {
		return err
	}
	defer e.Close()
	ctx, stop := ctxSignal()
	b, err := openBook(ctx, e, fs.Arg(0), true)
	stop()
	if err != nil {
		return err
	}
	m := b.Meta()
	fmt.Printf("\n«%s» — %d стр., %d разделов. Задавайте вопросы. /help — команды, /exit — выход.\n", m.Title, m.Pages, m.Sections)
	chat := "cli"
	in := bufio.NewReader(os.Stdin)
	for {
		fmt.Print("\nВы: ")
		line, err := in.ReadString('\n')
		line = strings.TrimSpace(line)
		if line == "" && err != nil {
			return nil
		}
		switch {
		case line == "":
			continue
		case line == "/exit" || line == "/quit":
			return nil
		case line == "/help":
			fmt.Println("/toc — оглавление\n/summary [N] — краткое содержание раздела N или всей книги\n/new — новый диалог\n/exit — выход\nCtrl+C во время ответа — остановить генерацию")
			continue
		case line == "/new":
			chat = "cli-" + strconv.FormatInt(time.Now().Unix(), 10)
			fmt.Println("Новый диалог.")
			continue
		case line == "/toc":
			printTOC(b)
			continue
		case strings.HasPrefix(line, "/summary"):
			sec := -1
			if f := strings.Fields(line); len(f) > 1 {
				if n, err := strconv.Atoi(f[1]); err == nil {
					sec = n
				}
			}
			qctx, qstop := ctxSignal()
			fmt.Print("\nИИ: ")
			_, err := b.SummarizeSection(qctx, sec, func(t string) { fmt.Print(t) })
			qstop()
			fmt.Println()
			if err != nil {
				fmt.Fprintln(os.Stderr, "Ошибка:", err)
			}
			continue
		}
		qctx, qstop := ctxSignal()
		fmt.Print("\nИИ: ")
		if err := ask(qctx, b, chat, line); err != nil && !errors.Is(err, context.Canceled) {
			fmt.Fprintln(os.Stderr, "Ошибка:", err)
		}
		qstop()
	}
}

func printTOC(b *locallm.Book) {
	for _, s := range b.TOC() {
		fmt.Printf("%s%3d. %s (стр. %d–%d)\n", strings.Repeat("  ", max(s.Level-1, 0)), s.ID, s.Title, s.PageFrom, s.PageTo)
	}
}

func cmdBooks(args []string) error {
	fs := flag.NewFlagSet("books", flag.ExitOnError)
	var c common
	c.bind(fs)
	fs.Parse(args)
	e, err := c.open()
	if err != nil {
		return err
	}
	defer e.Close()
	for _, m := range e.Books() {
		fmt.Printf("%s  %-40s %4d стр.  %s\n", m.ID, m.Title, m.Pages, m.Author)
	}
	return nil
}

func cmdTOC(args []string) error {
	fs := flag.NewFlagSet("toc", flag.ExitOnError)
	var c common
	c.bind(fs)
	fs.Parse(args)
	if fs.NArg() < 1 {
		return errors.New("укажите id книги")
	}
	e, err := c.open()
	if err != nil {
		return err
	}
	defer e.Close()
	b, err := e.OpenBook(fs.Arg(0))
	if err != nil {
		return err
	}
	printTOC(b)
	return nil
}

func cmdSummary(args []string) error {
	fs := flag.NewFlagSet("summary", flag.ExitOnError)
	var c common
	c.bind(fs)
	fs.Parse(args)
	if fs.NArg() < 1 {
		return errors.New("укажите id книги")
	}
	e, err := c.open()
	if err != nil {
		return err
	}
	defer e.Close()
	b, err := e.OpenBook(fs.Arg(0))
	if err != nil {
		return err
	}
	sec := -1
	if fs.NArg() > 1 {
		sec, _ = strconv.Atoi(fs.Arg(1))
	}
	ctx, stop := ctxSignal()
	defer stop()
	_, err = b.SummarizeSection(ctx, sec, func(t string) { fmt.Print(t) })
	fmt.Println()
	return err
}

func cmdServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	var c common
	c.bind(fs)
	addr := fs.String("addr", "127.0.0.1:8787", "адрес (только loopback)")
	origins := fs.String("origins", "", "разрешённые Origin через запятую (например wails://wails)")
	open := fs.Bool("open", false, "открыть интерфейс в браузере")
	fs.Parse(args)
	host, _, err := net.SplitHostPort(*addr)
	if err != nil || (host != "127.0.0.1" && host != "localhost" && host != "::1") {
		return errors.New("для безопасности сервер слушает только 127.0.0.1")
	}
	dd := c.data
	if dd == "" {
		dd = locallm.DefaultDataDir()
	}
	tok, err := server.LoadOrCreateToken(dd)
	if err != nil {
		return err
	}
	// Сначала поднимаем порт — чтобы адрес открывался сразу, а модели грузились в фоне.
	ln, err := net.Listen("tcp", *addr)
	if err != nil { // порт занят (например, вторая копия программы) — берём любой свободный
		ln, err = net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return err
		}
	}
	url := "http://" + ln.Addr().String() + "/"
	fmt.Printf("\nlocallm запущен: %s\nЗагружаю модели (при первом запуске — до минуты, особенно если антивирус проверяет файлы)...\nСтраница в браузере сама обновится, когда всё будет готово. Остановить программу: Ctrl+C.\n", url)

	bh := &bootHandler{}
	var eng *locallm.Engine
	go func() {
		e, err := c.open()
		if err != nil {
			bh.fail(err)
			fmt.Fprintln(os.Stderr, "\nОшибка:", err)
			return
		}
		eng = e
		s := server.New(e, tok)
		s.Version = version
		if *origins != "" {
			s.AllowOrigins = strings.Split(*origins, ",")
		}
		bh.ready(s.Handler())
		fmt.Printf("\nГотово! Интерфейс: %s\nAPI для других программ: %sv1, токен в файле %s\n", url, url, filepath.Join(dd, "server.token"))
	}()
	srv := &http.Server{Handler: bh, ReadHeaderTimeout: 10 * time.Second}
	ctx, stop := ctxSignal()
	defer stop()
	go func() { <-ctx.Done(); srv.Shutdown(context.Background()) }()
	if *open {
		go func() { time.Sleep(300 * time.Millisecond); openBrowser(url) }()
	}
	err = srv.Serve(ln)
	if eng != nil {
		eng.Close()
	}
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// bootHandler отвечает сразу после запуска: «модели загружаются» или причина ошибки;
// после готовности движка передаёт запросы настоящему обработчику.
type bootHandler struct {
	mu  sync.RWMutex
	h   http.Handler
	err error
}

func (b *bootHandler) ready(h http.Handler) { b.mu.Lock(); b.h = h; b.mu.Unlock() }
func (b *bootHandler) fail(err error)       { b.mu.Lock(); b.err = err; b.mu.Unlock() }

const bootCSS = `body{margin:0;background:#171717;color:#e8e8e8;font:16px/1.6 -apple-system,"Segoe UI",system-ui,sans-serif;display:grid;place-items:center;min-height:100vh}
.c{max-width:640px;padding:32px;border:1px solid #333;border-radius:16px;background:#1c1c1c}h1{margin:0 0 12px;font-size:22px}pre{white-space:pre-wrap;background:#111;padding:12px;border-radius:10px;color:#ffb4a9}
.sp{width:22px;height:22px;border:3px solid #2a8fd0;border-top-color:transparent;border-radius:50%;animation:s 1s linear infinite;display:inline-block;vertical-align:middle;margin-right:10px}@keyframes s{to{transform:rotate(360deg)}}`

func (b *bootHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b.mu.RLock()
	h, err := b.h, b.err
	b.mu.RUnlock()
	if h != nil {
		h.ServeHTTP(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if strings.HasPrefix(r.URL.Path, "/v1/") {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprint(w, `{"error":"модели ещё загружаются, подождите несколько секунд"}`)
		return
	}
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, `<!doctype html><meta charset=utf-8><title>locallm — ошибка</title><style>%s</style><div class=c><h1>Не удалось запустить модели</h1><pre>%s</pre><p>Исправьте причину и запустите программу снова. Проверить окружение можно командой <b>locallm doctor</b>.</p></div>`, bootCSS, html.EscapeString(err.Error()))
		return
	}
	fmt.Fprintf(w, `<!doctype html><meta charset=utf-8><meta http-equiv=refresh content=2><title>locallm — загрузка</title><style>%s</style><div class=c><h1><span class=sp></span>Загружаю модели…</h1>Это происходит один раз при запуске (до минуты). Страница обновится сама.</div>`, bootCSS)
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
}

func cmdPack(args []string) error {
	fs := flag.NewFlagSet("pack", flag.ExitOnError)
	out := fs.String("o", "", "выходной файл")
	fs.Parse(args)
	if *out == "" || fs.NArg() == 0 {
		return errors.New("использование: locallm pack -o locallm-bundle[.exe] llm.gguf embed.gguf")
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if err := payload.Pack(exe, *out, fs.Args()); err != nil {
		return err
	}
	st, _ := os.Stat(*out)
	fmt.Printf("Готово: %s (%.1f ГБ). Это один файл — ничего больше распространять не нужно.\n", *out, float64(st.Size())/(1<<30))
	return nil
}

func cmdDoctor(args []string) error {
	fs := flag.NewFlagSet("doctor", flag.ExitOnError)
	var c common
	c.bind(fs)
	fs.Parse(args)
	m := hw.Memory()
	fmt.Printf("ОС/арх: %s/%s\nCPU потоков: %d (будет использовано %d)\nRAM: %.1f ГБ (доступно %.1f)\n",
		runtime.GOOS, runtime.GOARCH, runtime.NumCPU(), hw.Threads(), float64(m.TotalRAM)/(1<<30), float64(m.AvailRAM)/(1<<30))
	fmt.Println("Форматы:", strings.Join(extract.Supported, " "))
	e, err := c.open()
	if err != nil {
		return err
	}
	defer e.Close()
	l, em := e.ModelInfo()
	fmt.Printf("LLM: %s\nЭмбеддинги: %s\nПапка данных: %s\n", l, em, locallm.DefaultDataDir())
	fmt.Println("OK: модели найдены и эмбеддинг-модель загружается.")
	return nil
}
