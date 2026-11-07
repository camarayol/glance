package glance

import (
	"testing"
	"time"
)

func TestWidgetUpdateInterval(t *testing.T) {
	c, err := newConfigFromYAML([]byte(`
pages:
  - name: Test
    columns:
      - size: full
        widgets:
          - type: clock
`))
	if err != nil {
		t.Fatalf("parsing config: %v", err)
	}

	if c.Server.WidgetUpdateInterval == nil {
		t.Fatal("expected widget-update-interval to have a default value")
	}

	if got := time.Duration(*c.Server.WidgetUpdateInterval); got != DEFAULT_WIDGET_UPDATE_INTERVAL {
		t.Errorf("expected default widget-update-interval of %v, got %v", DEFAULT_WIDGET_UPDATE_INTERVAL, got)
	}

	c, err = newConfigFromYAML([]byte(`
server:
  widget-update-interval: 5m
pages:
  - name: Test
    columns:
      - size: full
        widgets:
          - type: clock
`))
	if err != nil {
		t.Fatalf("parsing config: %v", err)
	}

	if got := time.Duration(*c.Server.WidgetUpdateInterval); got != 5*time.Minute {
		t.Errorf("expected widget-update-interval of %v, got %v", 5*time.Minute, got)
	}

	c, err = newConfigFromYAML([]byte(`
server:
  widget-update-interval: 0s
pages:
  - name: Test
    columns:
      - size: full
        widgets:
          - type: clock
`))
	if err != nil {
		t.Fatalf("parsing config: %v", err)
	}

	if got := time.Duration(*c.Server.WidgetUpdateInterval); got != 0 {
		t.Errorf("expected widget-update-interval of 0, got %v", got)
	}
}
