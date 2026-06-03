package interviewd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/google/uuid"
)

type interview struct {
	ID        string     `json:"id"`
	Questions []question `json:"questions"`
	Prepared  bool       `json:"prepared"`
}

type question struct {
	Body         string   `json:"body"`
	Kind         string   `json:"kind"`
	Options      []string `json:"options,omitempty"`
	WithTextarea bool     `json:"with_textarea,omitempty"`
}

type prepared struct {
	Port         int      `json:"port"`
	RenderedHTML []string `json:"rendered_html"`
}

type store struct {
	root string
}

func newInterviewID() (string, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return "", fmt.Errorf("create interview id: %w", err)
	}

	return "interview-" + id.String(), nil
}

func (s store) dir(id string) string {
	return filepath.Join(s.root, id)
}

func (s store) interviewPath(id string) string {
	return filepath.Join(s.dir(id), "interview.json")
}

func (s store) preparedPath(id string) string {
	return filepath.Join(s.dir(id), "prepared.json")
}

func (s store) saveInterview(iv *interview) error {
	if err := os.MkdirAll(s.dir(iv.ID), 0o700); err != nil {
		return fmt.Errorf("create interview state directory: %w", err)
	}

	return writeJSON(s.interviewPath(iv.ID), iv)
}

func (s store) loadInterview(id string) (*interview, error) {
	var iv interview
	if err := readJSON(s.interviewPath(id), &iv); err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("interview %q not found", id)
		}

		return nil, err
	}

	return &iv, nil
}

func (s store) savePrepared(id string, prepared *prepared) error {
	return writeJSON(s.preparedPath(id), prepared)
}

func (s store) loadPrepared(id string) (*prepared, error) {
	var prepared prepared
	if err := readJSON(s.preparedPath(id), &prepared); err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("interview %q is not prepared; run prepare-to-serve first", id)
		}

		return nil, err
	}

	return &prepared, nil
}

func (s store) deletePrepared(id string) error {
	if err := os.Remove(s.preparedPath(id)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("delete prepared state: %w", err)
	}

	return nil
}

func (s store) deleteInterview(id string) error {
	if err := os.RemoveAll(s.dir(id)); err != nil {
		return fmt.Errorf("delete interview state: %w", err)
	}

	return nil
}

func writeJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal JSON state: %w", err)
	}

	data = append(data, '\n')

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write temporary JSON state: %w", err)
	}

	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("replace JSON state: %w", err)
	}

	return nil
}

func readJSON(path string, v any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read JSON state: %w", err)
	}

	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("decode JSON state: %w", err)
	}

	return nil
}
