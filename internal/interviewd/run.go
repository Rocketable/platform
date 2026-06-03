// Package interviewd implements the interviewd command.
package interviewd

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
)

// Run executes interviewd with argv0 and args.
func Run(ctx context.Context, argv0 string, args []string) error {
	invocation := invocationCommand(argv0)

	if len(args) == 0 {
		fmt.Print(helpText(invocation))
		return nil
	}

	store := store{root: ".interviewd"}

	if args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		fmt.Print(helpText(invocation))
		return nil
	}

	if args[0] == "new" {
		id := "interview-" + rand.Text()
		if err := store.saveInterview(&interview{ID: id}); err != nil {
			return err
		}

		fmt.Printf("INTERVIEW_ID=%s\n", id)

		return nil
	}

	if len(args) < 2 {
		return fmt.Errorf("usage error; run `%s help` for examples", invocation)
	}

	id, subcmd, rest := args[0], args[1], args[2:]
	switch subcmd {
	case "add-question":
		if len(rest) < 2 {
			return fmt.Errorf("usage: %s <id> add-question <markdown> radio|checkbox|text [options...] [--with-textarea]", invocation)
		}

		return addQuestion(store, id, rest)
	case "delete-question":
		if len(rest) != 1 {
			return fmt.Errorf("usage: %s <id> delete-question <1-based-index>", invocation)
		}

		return deleteQuestion(store, id, rest)
	case "reorder-questions":
		return reorderQuestions(store, id, rest)
	case "preview":
		if len(rest) != 0 {
			return errors.New("preview takes no arguments")
		}

		iv, err := store.loadInterview(id)
		if err != nil {
			return err
		}

		fmt.Print(renderPreview(iv))

		return nil
	case "prepare-to-serve":
		if len(rest) != 0 {
			return errors.New("prepare-to-serve takes no arguments")
		}

		return prepareToServe(ctx, store, id, invocation, os.Stdout)
	case "wait-for-user":
		if len(rest) != 0 {
			return errors.New("wait-for-user takes no arguments")
		}

		return waitForUser(ctx, store, id, os.Stdout)
	case "discard-form":
		if len(rest) != 0 {
			return errors.New("discard-form takes no arguments")
		}

		return store.deleteInterview(id)
	}

	return fmt.Errorf("usage error; run `%s help` for examples", invocation)
}

func invocationCommand(argv0 string) string {
	if argv0 == "" {
		return "interviewd"
	}

	path := filepath.Clean(argv0)
	if filepath.Base(path) == "interviewd" {
		for dir := filepath.Dir(path); ; dir = filepath.Dir(dir) {
			if strings.HasPrefix(filepath.Base(dir), "go-build") {
				if info, ok := debug.ReadBuildInfo(); ok && info.Path != "" {
					return "go run " + info.Path
				}

				break
			}

			if parent := filepath.Dir(dir); parent == dir {
				break
			}
		}
	}

	return argv0
}

func helpText(cmd string) string {
	return fmt.Sprintf(`interviewd collects structured questions, serves them as a temporary local HTML form, and prints submitted answers as Markdown.

Successful flow:

  $ %[1]s new
  INTERVIEW_ID=interview-018fd30f-2e19-7b52-8f4e-0f455d7d5f02

  $ %[1]s "interview-018fd30f-2e19-7b52-8f4e-0f455d7d5f02" add-question "Which option should I use?" radio "option 1" "option 2" --with-textarea
  $ %[1]s "interview-018fd30f-2e19-7b52-8f4e-0f455d7d5f02" add-question "Which tasks should I do?" checkbox "task 1" "task 2"
  $ %[1]s "interview-018fd30f-2e19-7b52-8f4e-0f455d7d5f02" add-question "Anything else I should know?" text

  $ %[1]s "interview-018fd30f-2e19-7b52-8f4e-0f455d7d5f02" preview
  $ %[1]s "interview-018fd30f-2e19-7b52-8f4e-0f455d7d5f02" prepare-to-serve
  $ %[1]s "interview-018fd30f-2e19-7b52-8f4e-0f455d7d5f02" wait-for-user

Commands:

  %[1]s new
  %[1]s <id> add-question <markdown> radio <option...> [--with-textarea]
  %[1]s <id> add-question <markdown> checkbox <option...> [--with-textarea]
  %[1]s <id> add-question <markdown> text
  %[1]s <id> delete-question <1-based-index>
  %[1]s <id> reorder-questions <new-index-order...>
  %[1]s <id> preview
  %[1]s <id> prepare-to-serve
  %[1]s <id> wait-for-user
  %[1]s <id> discard-form

Notes:

  All questions are required. Checkbox questions require at least one selected option.
  Question Markdown is rendered through GitHub's Markdown API during prepare-to-serve.
  prepare-to-serve exits after printing URLs; call wait-for-user afterward with a long agent timeout, suggested: 1h.
  wait-for-user prints final answers to stdout as Markdown and deletes the interview after successful submission.
`, cmd)
}

func addQuestion(store store, id string, args []string) error {
	if len(args) < 2 {
		return errors.New("add-question requires <markdown> and radio|checkbox|text")
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

	q := question{Body: body, Kind: kind, Options: options, WithTextarea: withTextarea}
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
		return errors.New("delete-question requires exactly one 1-based index")
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
	reordered := make([]question, len(iv.Questions))

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
		return 0, errors.New("index must be a positive integer")
	}

	return n - 1, nil
}

func validateQuestion(q question) error {
	if strings.TrimSpace(q.Body) == "" {
		return errors.New("question body is required")
	}

	switch q.Kind {
	case "radio", "checkbox":
		if len(q.Options) == 0 {
			return fmt.Errorf("%s questions require at least one option", q.Kind)
		}

		seen := map[string]bool{}

		for _, option := range q.Options {
			if strings.TrimSpace(option) == "" {
				return errors.New("empty option is not allowed")
			}

			if seen[option] {
				return fmt.Errorf("duplicate option %q", option)
			}

			seen[option] = true
		}
	case "text":
		if len(q.Options) != 0 {
			return errors.New("text questions do not take options")
		}

		if q.WithTextarea {
			return errors.New("text questions are already textarea questions")
		}
	default:
		return fmt.Errorf("unknown question kind %q", q.Kind)
	}

	return nil
}
