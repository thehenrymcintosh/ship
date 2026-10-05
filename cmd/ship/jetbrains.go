package main

import (
	"fmt"
	"html"
	"os"
	"path/filepath"
	"strings"

	"github.com/thehenrymcintosh/ship/internal/brand"
)

// JetBrains IDEs keep JSON Schema mappings in .idea/jsonSchemas.xml. Each
// mapping names a schema file and glob patterns, relative to the project.

type ideaMapping struct {
	name     string
	schema   string   // relative to the project root
	patterns []string // relative to the project root
}

// ideaPatterns builds the mappings for a project rooted at projectRoot.
// schemaDir is where init wrote the schemas.
func ideaPatterns(projectRoot, root string, global bool) []ideaMapping {
	shipDir := filepath.Join(root, brand.Dir)
	if global {
		shipDir = root
	}
	relTo := func(p string) string {
		r, err := filepath.Rel(projectRoot, p)
		if err != nil {
			return p
		}
		return filepath.ToSlash(r)
	}
	pipes := relTo(filepath.Join(shipDir, "pipelines"))
	return []ideaMapping{
		{name: brand.Name + " pipeline", schema: relTo(filepath.Join(shipDir, "schema", "pipeline.json")),
			patterns: []string{pipes + "/*.yml", pipes + "/*.yaml"}},
		// Its own entry, so projects mapped before pipeline folders get it too.
		{name: brand.Name + " pipeline folders", schema: relTo(filepath.Join(shipDir, "schema", "pipeline.json")),
			patterns: []string{pipes + "/*/pipeline.yml"}},
		{name: brand.Name + " config", schema: relTo(filepath.Join(shipDir, "schema", "config.json")),
			patterns: []string{relTo(filepath.Join(shipDir, "config.yml"))}},
	}
}

func (m ideaMapping) entry() string {
	var b strings.Builder
	esc := html.EscapeString
	fmt.Fprintf(&b, "        <entry key=\"%s\">\n          <value>\n            <SchemaInfo>\n", esc(m.name))
	fmt.Fprintf(&b, "              <option name=\"name\" value=\"%s\" />\n", esc(m.name))
	fmt.Fprintf(&b, "              <option name=\"relativePathToSchema\" value=\"%s\" />\n", esc(m.schema))
	b.WriteString("              <option name=\"patterns\">\n                <list>\n")
	for _, p := range m.patterns {
		b.WriteString("                  <Item>\n")
		b.WriteString("                    <option name=\"pattern\" value=\"true\" />\n")
		fmt.Fprintf(&b, "                    <option name=\"path\" value=\"%s\" />\n", esc(p))
		b.WriteString("                    <option name=\"mappingKind\" value=\"Pattern\" />\n")
		b.WriteString("                  </Item>\n")
	}
	b.WriteString("                </list>\n              </option>\n            </SchemaInfo>\n          </value>\n        </entry>\n")
	return b.String()
}

// mergeJetBrains adds ship's schema mappings to <projectRoot>/.idea/
// jsonSchemas.xml, creating the file if needed. Existing mappings with the
// same names are left alone. It reports whether the file changed.
func mergeJetBrains(projectRoot, _ string, maps []ideaMapping) (bool, error) {
	path := filepath.Join(projectRoot, ".idea", "jsonSchemas.xml")
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		var entries strings.Builder
		for _, m := range maps {
			entries.WriteString(m.entry())
		}
		doc := "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<project version=\"4\">\n" +
			"  <component name=\"JsonSchemaMappingsProjectConfiguration\">\n    <state>\n      <map>\n" +
			entries.String() + "      </map>\n    </state>\n  </component>\n</project>\n"
		return true, os.WriteFile(path, []byte(doc), 0o644)
	}
	if err != nil {
		return false, err
	}
	s := string(data)
	var add strings.Builder
	for _, m := range maps {
		if !strings.Contains(s, `key="`+html.EscapeString(m.name)+`"`) {
			add.WriteString(m.entry())
		}
	}
	if add.Len() == 0 {
		return false, nil
	}
	switch {
	case strings.Contains(s, "</map>"):
		i := strings.LastIndex(s, "</map>")
		// Keep the closing tag's indentation.
		j := strings.LastIndex(s[:i], "\n") + 1
		s = s[:j] + add.String() + s[j:]
	case strings.Contains(s, `<component name="JsonSchemaMappingsProjectConfiguration" />`):
		s = strings.Replace(s, `<component name="JsonSchemaMappingsProjectConfiguration" />`,
			"<component name=\"JsonSchemaMappingsProjectConfiguration\">\n    <state>\n      <map>\n"+add.String()+"      </map>\n    </state>\n  </component>", 1)
	case strings.Contains(s, "</project>"):
		i := strings.LastIndex(s, "</project>")
		s = s[:i] + "  <component name=\"JsonSchemaMappingsProjectConfiguration\">\n    <state>\n      <map>\n" +
			add.String() + "      </map>\n    </state>\n  </component>\n" + s[i:]
	default:
		return false, fmt.Errorf("unrecognised format; add the mappings in Settings › Languages & Frameworks › Schemas")
	}
	return true, os.WriteFile(path, []byte(s), 0o644)
}
