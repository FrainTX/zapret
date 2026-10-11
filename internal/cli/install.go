package cli

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"prosvet/internal/config"
	"prosvet/internal/core"
	"prosvet/internal/daemon"
	"prosvet/internal/platform"
	"prosvet/internal/service"
	"prosvet/internal/ui"
)

func cmdInstall(rest []string) int {
	fs := flag.NewFlagSet("install", flag.ContinueOnError)
	printNix := fs.Bool("print-nix", false, "вывести модуль NixOS")
	yes := fs.Bool("yes", false, "не задавать вопросов")
	noTune := fs.Bool("no-tune", false, "не запускать подбор после установки")
	pause := fs.Bool("pause", false, "ждать Enter в конце (окно, открытое запросом прав)")
	if err := fs.Parse(rest); err != nil {
		return 2
	}
	if *pause {
		defer func() {
			fmt.Print(ui.Dim("\n  Enter — закрыть окно"))
			_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
		}()
	}
	info := platform.Detect()
	exe, err := os.Executable()
	if err != nil {
		return fail(err)
	}
	exe, _ = filepath.EvalSymlinks(exe)
	if *printNix {
		fmt.Print(service.NixModule(exe))
		return 0
	}
	if !info.Elevated {
		args := []string{"install"}
		if info.OS == "windows" {
			// Windows opens the elevated copy in its own window.
			args = append(args, "--pause")
		}
		return elevate(args)
	}
	if len(info.Missing) > 0 {
		for _, m := range info.Missing {
			fmt.Println(ui.Red("✕ ") + "не хватает " + m + ui.Dim("  → "+info.InstallHint(m)))
		}
		if !*yes && !confirm("Продолжить установку?", false) {
			return 1
		}
	}
	for _, c := range info.Conflicts {
		fmt.Println(ui.Amber("⚠ ") + c.Name + ui.Dim(" — "+c.Detail))
		if c.Fixable && (*yes || confirm("  Остановить и убрать из автозапуска?", true)) {
			if err := platform.FixConflict(c); err != nil {
				fmt.Println(ui.Red("  ✕ ") + err.Error())
			} else {
				ok("  остановлено")
			}
		}
	}

	fmt.Println(ui.Dim(" устанавливаю службу (" + info.Init + ")…"))
	st, err := service.Install(exe, info, ownerUID())
	if err != nil {
		return fail(err)
	}
	ok("служба установлена: " + st.Binary)
	if info.OS == "linux" {
		linkCommand(st.Binary, *yes)
	}

	cl := daemon.NewClient(config.DefaultPaths().Socket)
	deadline := time.Now().Add(20 * time.Second)
	for !cl.Available() {
		if time.Now().After(deadline) {
			return fail(fmt.Errorf("служба не отвечает — смотрите журнал: %s", config.DefaultPaths().LogFile))
		}
		time.Sleep(300 * time.Millisecond)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	s, err := cl.Status(ctx)
	cancel()
	if err != nil {
		return fail(err)
	}
	if !*noTune && (s.LastTune.IsZero() || (!*yes && confirm("Подобрать стратегии заново?", false))) {
		fmt.Println()
		if code := runTune(cl, core.TuneOptions{}); code != 0 {
			return code
		}
	} else if !s.Running {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		_ = cl.Start(ctx)
		cancel()
	}
	fmt.Println()
	ok("готово. Просвет работает и будет запускаться вместе с системой.")
	if info.OS == "linux" {
		fmt.Println("  Панель — из любой папки, без sudo: " + ui.Bold(service.Command))
	}
	return 0
}

// linkCommand makes "zapret" open the panel. If another program owns that
// name (typically an older zapret-discord-youtube), ask before moving it
// to zapret-old; Uninstall puts it back.
func linkCommand(bin string, yes bool) {
	rec, err := service.LinkCommand(bin, ownerUID(), yes)
	var taken *service.ErrCommandTaken
	if errors.As(err, &taken) {
		fmt.Println(ui.Amber("⚠ ") + "команда " + ui.Bold(service.Command) + " сейчас запускает " + taken.Target)
		if !confirm("  Сделать «"+service.Command+"» командой Просвета? Старая останется под именем "+service.Command+"-old", true) {
			fmt.Println(ui.Dim("  оставил как есть; Просвет открывается командой prosvet"))
			return
		}
		rec, err = service.LinkCommand(bin, ownerUID(), true)
	}
	if err != nil {
		fmt.Println(ui.Amber("⚠ ") + "команда " + service.Command + ": " + err.Error() + ui.Dim(" — используйте prosvet"))
		return
	}
	ok("команда " + service.Command + " → " + rec.Link)
	if rec.Old != "" {
		fmt.Println(ui.Dim("  прежняя команда теперь: " + rec.Old))
	}
}

func cmdUninstall(rest []string) int {
	fs := flag.NewFlagSet("uninstall", flag.ContinueOnError)
	purge := fs.Bool("purge", false, "удалить и настройки")
	if err := fs.Parse(rest); err != nil {
		return 2
	}
	info := platform.Detect()
	if !info.Elevated {
		args := []string{"uninstall"}
		if *purge {
			args = append(args, "--purge")
		}
		return elevate(args)
	}
	if err := service.Uninstall(info, *purge); err != nil {
		return fail(err)
	}
	ok("служба удалена")
	return 0
}
