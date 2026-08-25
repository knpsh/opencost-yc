package opencost

import "testing"

func TestParseYandexProvider(t *testing.T) {
	for _, input := range []string{"Yandex", "yandex cloud", "yc", "mks"} {
		if got := ParseProvider(input); got != YandexProvider {
			t.Errorf("ParseProvider(%q) = %q, want %q", input, got, YandexProvider)
		}
	}
}
