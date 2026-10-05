package gcp

import (
	"context"
	"strings"

	"skybuild/internal/model"
	"skybuild/internal/provider"
)

// AddKey authorizes another public key for the machine's user through instance metadata.
// The guest agent adds it to ~/.ssh/authorized_keys within a few seconds.
func (g *GCP) AddKey(ctx context.Context, m *model.Machine, publicKey string) error {
	var cur struct {
		Metadata struct {
			Items []struct{ Key, Value string }
		}
	}
	if err := g.cli.JSON(ctx, &cur, append([]string{"compute", "instances", "describe", m.InstanceID, "--format=json(metadata)"}, zoneArgs(m)...)...); err != nil {
		return err
	}
	keys := ""
	for _, it := range cur.Metadata.Items {
		if it.Key == "ssh-keys" {
			keys = it.Value
		}
	}
	line := m.User + ":" + strings.TrimSpace(publicKey)
	if strings.Contains(keys, line) {
		return nil
	}
	if keys != "" && !strings.HasSuffix(keys, "\n") {
		keys += "\n"
	}
	file, cleanup, err := tempKeys(keys + line + "\n")
	if err != nil {
		return err
	}
	defer cleanup()
	_, err = g.cli.Run(ctx, append([]string{"compute", "instances", "add-metadata", m.InstanceID, "--metadata-from-file", "ssh-keys=" + file}, zoneArgs(m)...)...)
	return err
}

func tempKeys(content string) (string, func(), error) {
	return provider.TempFile("sky-keys-*", content)
}
