package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
)

func main() {
	if err := run(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		fmt.Print(helpText())
		return nil
	}
	store := newStore(".interviewd")
	if args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		fmt.Print(helpText())
		return nil
	}
	if args[0] == "new" {
		id, err := newInterviewID()
		if err != nil {
			return err
		}
		if err := store.saveInterview(&Interview{ID: id}); err != nil {
			return err
		}
		fmt.Printf("INTERVIEW_ID=%s\n", id)
		return nil
	}
	if len(args) < 2 {
		return usageError()
	}
	id, cmd, rest := args[0], args[1], args[2:]
	switch cmd {
	case "add-question":
		return addQuestion(store, id, rest)
	case "delete-question":
		return deleteQuestion(store, id, rest)
	case "reorder-questions":
		return reorderQuestions(store, id, rest)
	case "preview":
		if len(rest) != 0 {
			return fmt.Errorf("preview takes no arguments")
		}
		iv, err := store.loadInterview(id)
		if err != nil {
			return err
		}
		fmt.Print(renderPreview(iv))
		return nil
	case "prepare-to-serve":
		if len(rest) != 0 {
			return fmt.Errorf("prepare-to-serve takes no arguments")
		}
		return prepareToServe(ctx, store, id, os.Stdout)
	case "wait-for-user":
		if len(rest) != 0 {
			return fmt.Errorf("wait-for-user takes no arguments")
		}
		return waitForUser(ctx, store, id, os.Stdout, os.Stderr)
	case "discard-form":
		if len(rest) != 0 {
			return fmt.Errorf("discard-form takes no arguments")
		}
		return store.deleteInterview(id)
	default:
		return usageError()
	}
}

func usageError() error {
	return fmt.Errorf("usage error; run `interviewd help` for examples")
}

func helpText() string {
	return `interviewd collects structured questions, serves them as a temporary local HTML form, and prints submitted answers as Markdown.

Successful flow:

  $ interviewd new
  INTERVIEW_ID=interview-018fd30f-2e19-7b52-8f4e-0f455d7d5f02

  $ interviewd "interview-018fd30f-2e19-7b52-8f4e-0f455d7d5f02" add-question "Which option should I use?" radio "option 1" "option 2" --with-textarea
  $ interviewd "interview-018fd30f-2e19-7b52-8f4e-0f455d7d5f02" add-question "Which tasks should I do?" checkbox "task 1" "task 2"
  $ interviewd "interview-018fd30f-2e19-7b52-8f4e-0f455d7d5f02" add-question "Anything else I should know?" text

  $ interviewd "interview-018fd30f-2e19-7b52-8f4e-0f455d7d5f02" preview
  $ interviewd "interview-018fd30f-2e19-7b52-8f4e-0f455d7d5f02" prepare-to-serve
  $ interviewd "interview-018fd30f-2e19-7b52-8f4e-0f455d7d5f02" wait-for-user

Commands:

  interviewd new
  interviewd <id> add-question <markdown> radio <option...> [--with-textarea]
  interviewd <id> add-question <markdown> checkbox <option...> [--with-textarea]
  interviewd <id> add-question <markdown> text
  interviewd <id> delete-question <1-based-index>
  interviewd <id> reorder-questions <new-index-order...>
  interviewd <id> preview
  interviewd <id> prepare-to-serve
  interviewd <id> wait-for-user
  interviewd <id> discard-form

Notes:

  All questions are required. Checkbox questions require at least one selected option.
  Question Markdown is rendered through GitHub's Markdown API during prepare-to-serve.
  prepare-to-serve exits after printing URLs; call wait-for-user afterward with a long agent timeout, suggested: 1h.
  wait-for-user prints final answers to stdout as Markdown and deletes the interview after successful submission.
`
}

func addQuestion(store store, id string, args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: interviewd <id> add-question <markdown> radio|checkbox|text [options...] [--with-textarea]")
	}
	body, kind := args[0], args[1]
	withTextarea := false
	options := make([]string, 0, len(args)-2)
	for _, arg := range args[2:] {
		if arg == "--with-textarea" {
			withTextarea = true
			continue
		}
		options = append(options, arg)
	}
	q := Question{Body: body, Kind: kind, Options: options, WithTextarea: withTextarea}
	if err := validateQuestion(q); err != nil {
		return err
	}
	iv, err := store.loadInterview(id)
	if err != nil {
		return err
	}
	iv.Questions = append(iv.Questions, q)
	iv.Prepared = false
	return store.saveInterview(iv)
}

func deleteQuestion(store store, id string, args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: interviewd <id> delete-question <1-based-index>")
	}
	idx, err := parseIndex(args[0])
	if err != nil {
		return err
	}
	iv, err := store.loadInterview(id)
	if err != nil {
		return err
	}
	if idx < 0 || idx >= len(iv.Questions) {
		return fmt.Errorf("question index %d out of range", idx+1)
	}
	iv.Questions = append(iv.Questions[:idx], iv.Questions[idx+1:]...)
	iv.Prepared = false
	if err := store.saveInterview(iv); err != nil {
		return err
	}
	return store.deletePrepared(id)
}

func reorderQuestions(store store, id string, args []string) error {
	iv, err := store.loadInterview(id)
	if err != nil {
		return err
	}
	if len(args) != len(iv.Questions) {
		return fmt.Errorf("reorder-questions requires exactly %d indexes", len(iv.Questions))
	}
	seen := make([]bool, len(iv.Questions))
	reordered := make([]Question, len(iv.Questions))
	for dst, arg := range args {
		idx, err := parseIndex(arg)
		if err != nil {
			return err
		}
		if idx < 0 || idx >= len(iv.Questions) {
			return fmt.Errorf("question index %d out of range", idx+1)
		}
		if seen[idx] {
			return fmt.Errorf("question index %d appears more than once", idx+1)
		}
		seen[idx] = true
		reordered[dst] = iv.Questions[idx]
	}
	iv.Questions = reordered
	iv.Prepared = false
	if err := store.saveInterview(iv); err != nil {
		return err
	}
	return store.deletePrepared(id)
}

func parseIndex(s string) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 {
		return 0, fmt.Errorf("index must be a positive integer")
	}
	return n - 1, nil
}

func validateQuestion(q Question) error {
	if strings.TrimSpace(q.Body) == "" {
		return fmt.Errorf("question body is required")
	}
	switch q.Kind {
	case "radio", "checkbox":
		if len(q.Options) == 0 {
			return fmt.Errorf("%s questions require at least one option", q.Kind)
		}
		seen := map[string]bool{}
		for _, option := range q.Options {
			if strings.TrimSpace(option) == "" {
				return fmt.Errorf("empty option is not allowed")
			}
			if seen[option] {
				return fmt.Errorf("duplicate option %q", option)
			}
			seen[option] = true
		}
	case "text":
		if len(q.Options) != 0 {
			return fmt.Errorf("text questions do not take options")
		}
		if q.WithTextarea {
			return fmt.Errorf("text questions are already textarea questions")
		}
	default:
		return fmt.Errorf("unknown question kind %q", q.Kind)
	}
	return nil
}
