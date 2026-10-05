package bcc

import (
	"strings"
	"testing"
)

func TestDashboardPollingBudgetAndVisibility(t *testing.T) {
	checks := []string{
		"const fastPollMS=10000,slowPollMS=30000",
		"async function loadFast(){if(document.hidden)return;await Promise.all([loadMonitoring(),loadTunnels(),loadHealth()])}",
		"async function loadSlow(){if(document.hidden)return;await Promise.all([loadFinance(),loadDiscovery()])}",
		"function stopPolling(){if(fastPollTimer!==null){clearInterval(fastPollTimer);fastPollTimer=null}if(slowPollTimer!==null){clearInterval(slowPollTimer);slowPollTimer=null}}",
		"function startPolling(){stopPolling();if(document.hidden)return;fastPollTimer=setInterval(loadFast,fastPollMS);slowPollTimer=setInterval(loadSlow,slowPollMS)}",
		"document.addEventListener('visibilitychange'",
	}
	for _, want := range checks {
		if !strings.Contains(dashboardHTML, want) {
			t.Fatalf("dashboard polling contract missing %q", want)
		}
	}
	if strings.Contains(dashboardHTML, "setInterval(()=>{loadMonitoring();loadFinance();loadTunnels();loadHealth();loadDiscovery()},5000)") {
		t.Fatal("legacy 5-second five-endpoint polling loop still present")
	}

	const requestsPerMinute = 3*(60000/10000) + 2*(60000/30000)
	if requestsPerMinute != 22 {
		t.Fatalf("recurring dashboard request budget=%d want 22", requestsPerMinute)
	}
	if requestsPerMinute >= 120 {
		t.Fatalf("recurring dashboard request budget=%d must stay below rate-limit 120/min", requestsPerMinute)
	}
}
