package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestImportsConform(t *testing.T) {
	tests := []struct {
		name   string
		source string
		want   bool
	}{
		{
			name: "three groups",
			source: `package example
import (
	"fmt"
	"os"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
)
`,
			want: true,
		},
		{
			name: "only external imports",
			source: `package example
import (
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
)
`,
			want: true,
		},
		{
			name: "mixed local and external",
			source: `package example
import (
	"github.com/trickstercache/trickster/v2/pkg/backends"
	"github.com/stretchr/testify/assert"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
)
`,
			want: false,
		},
		{
			name: "missing blank line",
			source: `package example
import (
	"fmt"
	"github.com/trickstercache/trickster/v2/pkg/backends"
)
`,
			want: false,
		},
		{
			name: "unnecessary blank line",
			source: `package example
import (
	"fmt"

	"os"
)
`,
			want: false,
		},
		{
			name: "too many blank lines between groups",
			source: `package example
import (
	"fmt"


	"github.com/trickstercache/trickster/v2/pkg/backends"
)
`,
			want: false,
		},
		{
			name: "wrong group order",
			source: `package example
import (
	"github.com/stretchr/testify/assert"

	"github.com/trickstercache/trickster/v2/pkg/backends"
)
`,
			want: false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "example.go")
			if err := os.WriteFile(path, []byte(test.source), 0o600); err != nil {
				t.Fatal(err)
			}

			got, err := importsConform(path)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("importsConform() = %t, want %t", got, test.want)
			}
		})
	}
}
