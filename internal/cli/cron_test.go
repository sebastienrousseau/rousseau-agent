package cli

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCronAddCmd_MissingFields(t *testing.T) {
	opts := makeOpts(t)
	cmd := newCronAddCmd(opts)
	cmd.SetContext(context.Background())
	err := cmd.RunE(cmd, nil)
	assert.Error(t, err)
}

func TestCronAddCmd_InvalidSchedule(t *testing.T) {
	opts := makeOpts(t)
	cmd := newCronAddCmd(opts)
	require.NoError(t, cmd.Flags().Set("name", "test"))
	require.NoError(t, cmd.Flags().Set("schedule", "bad-expr"))
	require.NoError(t, cmd.Flags().Set("prompt", "hi"))
	cmd.SetContext(context.Background())
	err := cmd.RunE(cmd, nil)
	assert.Error(t, err)
}

func TestCronAddCmd_HappyPath(t *testing.T) {
	opts := makeOpts(t)
	cmd := newCronAddCmd(opts)
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	require.NoError(t, cmd.Flags().Set("name", "morning"))
	require.NoError(t, cmd.Flags().Set("schedule", "0 8 * * *"))
	require.NoError(t, cmd.Flags().Set("prompt", "brief"))
	require.NoError(t, cmd.Flags().Set("deliver-to", "user@x"))
	cmd.SetContext(context.Background())
	require.NoError(t, cmd.RunE(cmd, nil))
	assert.Contains(t, buf.String(), "added morning")
}

func TestCronListCmd_Empty(t *testing.T) {
	opts := makeOpts(t)
	cmd := newCronListCmd(opts)
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	cmd.SetContext(context.Background())
	require.NoError(t, cmd.RunE(cmd, nil))
	assert.Contains(t, buf.String(), "no jobs")
}

func TestCronListCmd_ShowsAddedJob(t *testing.T) {
	opts := makeOpts(t)
	// Add via the CLI so the store gets initialised the same way.
	add := newCronAddCmd(opts)
	require.NoError(t, add.Flags().Set("name", "row"))
	require.NoError(t, add.Flags().Set("schedule", "0 * * * *"))
	require.NoError(t, add.Flags().Set("prompt", "p"))
	add.SetContext(context.Background())
	require.NoError(t, add.RunE(add, nil))

	list := newCronListCmd(opts)
	buf := &bytes.Buffer{}
	list.SetOut(buf)
	list.SetContext(context.Background())
	require.NoError(t, list.RunE(list, nil))
	assert.Contains(t, buf.String(), "row")
}

func TestCronRemoveCmd(t *testing.T) {
	opts := makeOpts(t)
	add := newCronAddCmd(opts)
	require.NoError(t, add.Flags().Set("name", "kill-me"))
	require.NoError(t, add.Flags().Set("schedule", "0 * * * *"))
	require.NoError(t, add.Flags().Set("prompt", "p"))
	add.SetContext(context.Background())
	require.NoError(t, add.RunE(add, nil))

	rm := newCronRemoveCmd(opts)
	rm.SetContext(context.Background())
	require.NoError(t, rm.RunE(rm, []string{"kill-me"}))
}

func TestCronToggleCmd(t *testing.T) {
	opts := makeOpts(t)
	add := newCronAddCmd(opts)
	require.NoError(t, add.Flags().Set("name", "tog"))
	require.NoError(t, add.Flags().Set("schedule", "0 * * * *"))
	require.NoError(t, add.Flags().Set("prompt", "p"))
	add.SetContext(context.Background())
	require.NoError(t, add.RunE(add, nil))

	off := newCronToggleCmd(opts, false)
	off.SetContext(context.Background())
	require.NoError(t, off.RunE(off, []string{"tog"}))

	on := newCronToggleCmd(opts, true)
	on.SetContext(context.Background())
	require.NoError(t, on.RunE(on, []string{"tog"}))
}

func TestNewCronCmd_HasSubcommands(t *testing.T) {
	cmd := newCronCmd(&Options{})
	names := map[string]bool{}
	for _, c := range cmd.Commands() {
		names[c.Name()] = true
	}
	assert.True(t, names["add"])
	assert.True(t, names["list"])
	assert.True(t, names["remove"])
	assert.True(t, names["enable"])
	assert.True(t, names["disable"])
}
