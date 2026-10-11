// Command genipsets refreshes the IP snapshot embedded into prosvet
// (internal/lists/data/ip). Run it before a release:
//
//	go run ./tools/genipsets
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"prosvet/internal/lists"
)

func main() {
	dir := filepath.Join("internal", "lists", "data", "ip")
	if len(os.Args) > 1 {
		dir = os.Args[1]
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	var ids []string
	for _, p := range lists.Providers {
		ids = append(ids, p.ID)
	}
	f := &lists.Fetcher{}
	got, errs := f.FetchAll(ctx, ids, func(id string, n int, err error) {
		if err != nil {
			fmt.Printf("  %-13s ошибка: %v\n", id, err)
			return
		}
		fmt.Printf("  %-13s %6d префиксов\n", id, n)
	})
	if len(errs) > 0 {
		fmt.Fprintf(os.Stderr, "не получены: %d провайдеров — снимок не обновлён\n", len(errs))
		os.Exit(1)
	}
	total := 0
	for id, ps := range got {
		if err := lists.WriteGzipFile(filepath.Join(dir, id+".txt.gz"), lists.FormatPrefixes(ps)); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		total += len(ps)
	}
	date := time.Now().UTC().Format("2006-01-02")
	if err := os.WriteFile(filepath.Join(dir, "DATE"), []byte(date+"\n"), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("готово: %d префиксов, снимок от %s\n", total, date)
}
