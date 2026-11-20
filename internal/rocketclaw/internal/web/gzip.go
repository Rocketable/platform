//go:build ignore

package main

import (
	"bytes"
	"compress/gzip"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	for _, dir := range os.Args[1:] {
		err := filepath.WalkDir(dir, func(name string, entry fs.DirEntry, err error) error {
			if err != nil || entry.IsDir() || strings.HasSuffix(name, ".gz") {
				return err
			}

			data, err := os.ReadFile(name)
			if err != nil {
				return err
			}

			var compressed bytes.Buffer

			writer, _ := gzip.NewWriterLevel(&compressed, gzip.BestCompression)
			_, _ = writer.Write(data)
			_ = writer.Close()

			if err = os.WriteFile(name+".gz", compressed.Bytes(), 0o644); err != nil {
				return err
			}

			return os.Remove(name)
		})
		if err != nil {
			log.Fatal(err)
		}
	}
}
