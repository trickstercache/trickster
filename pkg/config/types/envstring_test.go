package types

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEnvString(t *testing.T) {
	os.Setenv("FOO", "bar")
	os.Setenv("BAR", "baz")
	example := EnvString("")
	// expect bar
	err := example.Unmarshal([]byte("${FOO}"))
	require.NoError(t, err)
	require.Equal(t, "bar", string(example))
	// expect baz
	err = example.Unmarshal([]byte("${BAR}"))
	require.NoError(t, err)
	require.Equal(t, "baz", string(example))
	// expect both
	err = example.Unmarshal([]byte("${FOO}${BAR}"))
	require.NoError(t, err)
	require.Equal(t, "barbaz", string(example))
}

func TestEnvStringMap(t *testing.T) {
	os.Setenv("FIZZ", "buzz")
	os.Setenv("BIZZ", "quux")
	example := EnvStringMap{}
	// expect fizz
	err := example.Unmarshal([]byte(`abc: "${FIZZ}"`))
	require.NoError(t, err)
	require.Equal(t, "buzz", example["abc"])
	// expect bizz
	err = example.Unmarshal([]byte(`def: "${BIZZ}"`))
	require.NoError(t, err)
	require.Equal(t, "quux", example["def"])
	// expect both
	err = example.Unmarshal([]byte(`abc: "${FIZZ}"` + "\n" + `def: "${BIZZ}"`))
	require.NoError(t, err)
	require.Equal(t, "buzz", example["abc"])
	require.Equal(t, "quux", example["def"])
}
