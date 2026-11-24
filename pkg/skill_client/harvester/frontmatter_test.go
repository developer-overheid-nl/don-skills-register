package harvester

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseSkillMarkdown(t *testing.T) {
	doc, err := ParseSkillMarkdown([]byte("\xef\xbb\xbf---\r\nname: ls-api\r\ndescription: \"API Design Rules\"\r\nallowed-tools:\r\n  - WebFetch(*)\r\n---\r\n# Titel\r\n"))
	require.NoError(t, err)
	assert.Equal(t, "ls-api", doc.Name)
	assert.Equal(t, "API Design Rules", doc.Description)
	assert.Equal(t, []any{"WebFetch(*)"}, doc.Frontmatter["allowed-tools"])
	assert.Equal(t, "# Titel\n", doc.Body)
}

func TestParseSkillMarkdownErrors(t *testing.T) {
	cases := map[string]string{
		"geen frontmatter":      "# Titel\n",
		"niet afgesloten":       "---\nname: x\n",
		"ongeldige yaml":        "---\nname: [x\n---\n",
		"name ontbreekt":        "---\ndescription: y\n---\n",
		"description ontbreekt": "---\nname: x\n---\n",
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ParseSkillMarkdown([]byte(input))
			assert.Error(t, err)
		})
	}
}
