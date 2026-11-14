package harness

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// frontmatterRead is one agent frontmatter key that a source file reads.
type frontmatterRead struct{ file, key string }

// frontmatterReads finds the keys files, by path, read from agent frontmatter: string literals or constants
// passed to frontmatterField, frontmatterValue and argString(frontmatter, …), index reads on an agent's
// Frontmatter and on maps taken from it, and the yaml tags of structs in files that read frontmatter. It
// matches names, not types: a key held in a variable is not seen.
func frontmatterReads(files map[string]*ast.File) map[frontmatterRead]bool {
	consts := map[string]string{}

	for _, file := range files {
		ast.Inspect(file, func(node ast.Node) bool {
			if spec, ok := node.(*ast.ValueSpec); ok && len(spec.Values) == len(spec.Names) {
				for i, name := range spec.Names {
					if lit, ok := spec.Values[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
						consts[name.Name], _ = strconv.Unquote(lit.Value)
					}
				}
			}

			return true
		})
	}

	key := func(expr ast.Expr) (string, bool) {
		if lit, ok := expr.(*ast.BasicLit); ok && lit.Kind == token.STRING {
			value, err := strconv.Unquote(lit.Value)
			return value, err == nil
		}

		ident, ok := expr.(*ast.Ident)
		if !ok {
			return "", false
		}

		value, ok := consts[ident.Name]

		return value, ok
	}

	yamlTag := regexp.MustCompile(`yaml:"([^",]+)`)

	reads := map[frontmatterRead]bool{}

	for path, file := range files {
		var keys, tags []string

		for _, decl := range file.Decls {
			derived := map[string]bool{}
			fromFrontmatter := func(expr ast.Expr) bool {
				switch expr := expr.(type) {
				case *ast.SelectorExpr:
					return expr.Sel.Name == "Frontmatter"
				case *ast.Ident:
					return derived[expr.Name]
				}

				return false
			}

			ast.Inspect(decl, func(node ast.Node) bool {
				switch node := node.(type) {
				case *ast.CallExpr:
					fn, _ := node.Fun.(*ast.Ident)
					if fn == nil || len(node.Args) < 2 {
						break
					}

					first, _ := node.Args[0].(*ast.Ident)
					if value, ok := key(node.Args[1]); ok && (fn.Name == "frontmatterField" || fn.Name == "frontmatterValue" || fn.Name == "argString" && first != nil && first.Name == "frontmatter") {
						keys = append(keys, value)
					}
				case *ast.IndexExpr:
					if value, ok := key(node.Index); ok && fromFrontmatter(node.X) {
						keys = append(keys, value)
					}
				case *ast.AssignStmt:
					rhs := node.Rhs[0]
					if assertion, ok := rhs.(*ast.TypeAssertExpr); ok {
						rhs = assertion.X
					}

					if index, ok := rhs.(*ast.IndexExpr); ok && fromFrontmatter(index.X) {
						if ident, ok := node.Lhs[0].(*ast.Ident); ok {
							derived[ident.Name] = true
						}
					}
				case *ast.Field:
					if node.Tag != nil {
						tag, _ := strconv.Unquote(node.Tag.Value)
						if match := yamlTag.FindStringSubmatch(tag); match != nil {
							tags = append(tags, match[1])
						}
					}
				}

				return true
			})
		}

		if len(keys) > 0 {
			keys = append(keys, tags...)
		}

		for _, value := range keys {
			reads[frontmatterRead{file: path, key: value}] = true
		}
	}

	return reads
}

func parseGoFiles(t *testing.T, dirs ...string) map[string]*ast.File {
	t.Helper()

	files := map[string]*ast.File{}
	fset := token.NewFileSet()

	for _, dir := range dirs {
		paths, err := filepath.Glob(filepath.Join(dir, "*.go"))
		require.NoError(t, err)

		for _, path := range paths {
			if strings.HasSuffix(path, "_test.go") {
				continue
			}

			// The scan reads Go source, which lives outside any sandbox.
			src, err := os.ReadFile(path)
			require.NoError(t, err)

			files[path], err = parser.ParseFile(fset, path, src, parser.SkipObjectResolution)
			require.NoError(t, err)
		}
	}

	return files
}

func TestAgentSettingsCoverEasyKeys(t *testing.T) {
	var lifted, help []string

	for parent, keys := range easyKeys() {
		for _, key := range keys {
			lifted = append(lifted, strings.TrimPrefix(parent+"."+key, "."))
		}
	}

	settings := AgentSettings()
	for i := range settings {
		if slices.Contains(settings[i].Modes, AgentModeEasy) {
			help = append(help, settings[i].Key)
		}
	}

	require.ElementsMatch(t, lifted, help)
}

func TestAgentSettingsCoverLoaderReads(t *testing.T) {
	settings := AgentSettings()

	var missing []string

	for read := range frontmatterReads(parseGoFiles(t, ".", "../backend")) {
		// Easy-only code lives in easy.go; every other read is an advanced-mode read.
		mode := AgentModeAdvanced
		if read.file == "easy.go" {
			mode = AgentModeEasy
		}

		found := false
		for i := range settings {
			found = found || slices.Contains(settings[i].Modes, mode) && (settings[i].Key == read.key || strings.HasSuffix(settings[i].Key, "."+read.key))
		}

		if !found {
			missing = append(missing, string(mode)+" "+read.key+" ("+read.file+")")
		}
	}

	slices.Sort(missing)
	require.Empty(t, missing, "frontmatter keys without help in AgentSettings")
}

func TestFrontmatterReadsSeeEveryShape(t *testing.T) {
	for name, src := range map[string]string{
		"direct call":    `package p; func f(node *yaml.Node) { _ = frontmatterField(node, "fooBar") }`,
		"derived map":    `package p; const fooKey = "fooBar"; func f(a Agent) { m, _ := a.Frontmatter["permission"].(map[string]any); _ = m[fooKey] }`,
		"decoded struct": "package p; type s struct { Foo string `yaml:\"fooBar\"` }; func f(node *yaml.Node) { _ = frontmatterField(node, \"model\") }",
	} {
		t.Run(name, func(t *testing.T) {
			file, err := parser.ParseFile(token.NewFileSet(), "fixture.go", src, parser.SkipObjectResolution)
			require.NoError(t, err)
			require.True(t, frontmatterReads(map[string]*ast.File{"fixture.go": file})[frontmatterRead{file: "fixture.go", key: "fooBar"}])
		})
	}
}

func TestAgentSettingsHaveHelpText(t *testing.T) {
	settings := AgentSettings()
	require.NotEmpty(t, settings)

	for i := range settings {
		setting := &settings[i]
		require.NotEmpty(t, setting.Modes, setting.Key)
		require.NotEmpty(t, setting.Group, setting.Key)

		for _, text := range []string{setting.Label, setting.Explanation, setting.Default, setting.Allowed} {
			require.NotEmpty(t, strings.TrimSpace(text), setting.Key)
		}

		require.Equal(t, setting.Control == AgentSettingChoice, len(setting.Choices) > 0, setting.Key)
	}
}
