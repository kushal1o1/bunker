package backup

import "fmt"

func HumanBytes(n int64) string {
	f := float64(n)
	for _, unit := range []string{"B", "KB", "MB", "GB", "TB"} {
		if f < 1024 || unit == "TB" {
			if unit == "B" {
				return fmt.Sprintf("%d %s", n, unit)
			}
			return fmt.Sprintf("%.1f %s", f, unit)
		}
		f /= 1024
	}
	return fmt.Sprintf("%d %s", n, "B")
}
