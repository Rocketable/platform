package main

import (
	"fmt"
	"strings"
)

func renderPreview(iv *Interview) string {
	var b strings.Builder
	for i, q := range iv.Questions {
		if i > 0 {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "## Question %d\n", i+1)
		b.WriteString(q.Body)
		b.WriteString("\n\n")
		switch q.Kind {
		case "radio":
			for _, option := range q.Options {
				fmt.Fprintf(&b, "( ) %s\n", option)
			}
		case "checkbox":
			for _, option := range q.Options {
				fmt.Fprintf(&b, "[ ] %s\n", option)
			}
		case "text":
			b.WriteString("[ textbox ]\n")
		}
		if q.WithTextarea {
			b.WriteString("\n[ with-textarea ]\n")
		}
	}
	if len(iv.Questions) > 0 {
		b.WriteByte('\n')
	}
	b.WriteString("{{ SUBMIT }}\n")
	return b.String()
}
