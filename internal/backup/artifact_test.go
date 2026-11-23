package backup

import (
	"testing"

	"github.com/kushal1o1/bunker/internal/execx"
)

// The exact command strings sent over SSH. A change here changes what runs
// on every server, so it must be deliberate.
func TestArtifactCommandsOverSSH(t *testing.T) {
	cases := []struct {
		art  Artifact
		want string
	}{
		{&PostgresArtifact{DB: "shop_db"}, "pg_dump -Fc -w shop_db"},
		{&PostgresArtifact{DB: "shop_db", Host: "127.0.0.1", Port: 5433, User: "bunker"},
			"pg_dump -Fc -w -h 127.0.0.1 -p 5433 -U bunker shop_db"},
		{&FilesArtifact{RemotePath: "/var/www/shop/uploads"}, "tar -C /var/www/shop -cf - uploads"},
		{&FilesArtifact{RemotePath: "/srv/my files/$HOME"}, "tar -C '/srv/my files' -cf - '$HOME'"},
		{&FilesArtifact{RemotePath: "/data"}, "tar -C / -cf - data"},
	}
	for _, tc := range cases {
		if got := execx.ShellJoin(tc.art.Argv()); got != tc.want {
			t.Errorf("%s: command = %q, want %q", tc.art.ID(), got, tc.want)
		}
	}
}
