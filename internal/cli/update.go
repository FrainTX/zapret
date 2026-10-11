package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"time"

	"prosvet/internal/core"
	"prosvet/internal/ui"
	"prosvet/internal/update"
)

// cmdUpdate: prosvet update [--check] [--auto on|off] [--mirror URL]
func cmdUpdate(rest []string) int {
	fs := flag.NewFlagSet("update", flag.ContinueOnError)
	check := fs.Bool("check", false, "только проверить")
	auto := fs.String("auto", "", "автообновление: on|off")
	mirror := fs.String("mirror", "-", "своё зеркало (https://…), пусто — убрать")
	if err := fs.Parse(rest); err != nil {
		return 2
	}
	a, _, err := connect()
	if err != nil {
		return fail(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	if *auto != "" || *mirror != "-" {
		var s core.Settings
		if *auto != "" {
			on := *auto == "on"
			if !on && *auto != "off" {
				return fail(errors.New("--auto on|off"))
			}
			s.AutoUpdate = &on
		}
		if *mirror != "-" {
			s.UpdateMirror = mirror
		}
		if err := a.Settings(ctx, s); err != nil {
			return fail(err)
		}
		ok("настройки обновлений сохранены")
		return 0
	}
	fmt.Println(ui.Dim(" текущая версия ") + ui.Bold(core.Version) + ui.Dim(" · проверяю…"))
	st, err := a.Update(ctx, false)
	if err != nil {
		if errors.Is(err, update.ErrNoRelease) {
			fmt.Println(ui.Dim("  релизов пока нет"))
			return 0
		}
		return fail(err)
	}
	if !st.Available {
		ok("установлена последняя версия (" + st.Current + ")")
		return 0
	}
	fmt.Println(ui.Accent("  доступна версия ") + ui.Bold(st.Latest))
	if st.Notes != "" {
		fmt.Println(ui.Dim("  " + ui.Trunc(st.Notes, 300)))
	}
	if *check {
		return 0
	}
	if _, err := a.Update(ctx, true); err != nil {
		return fail(err)
	}
	ok("версия " + st.Latest + " установлена, служба перезапускается")
	return 0
}
