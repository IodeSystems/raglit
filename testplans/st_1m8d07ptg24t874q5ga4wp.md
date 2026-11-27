# Test plan: st_1m8d07ptg24t874q5ga4wp

Verify the aw4 KPI report implementation locations and the ordering problem: the full KPI computation (SprintRetroKPIs) runs before metric-path validation (measureCause), and the key layout is data-dependent (omitempty tags in retro.go).

```sh
set -e
AW4="$HOME/local/src/iodesystems/aw4"

# Verify the aw4 checkout exists and the specific files are at the claimed paths
[ -f "$AW4/internal/command/improvements.go" ]
[ -f "$AW4/internal/command/retro.go" ]
[ -f "$AW4/internal/command/kpiweb.go" ]
echo "PASS: all claimed files exist"

# Verify the key symbols are in improvements.go
for sym in metricParts readMetric arrayStep measureCause arrayKeys SprintRetroKPIs; do
  grep -q "$sym" "$AW4/internal/command/improvements.go"
done
echo "PASS: all key symbols found in improvements.go"

# Verify omitempty is present in retro.go (the data-dependent key layout finding)
grep -q 'omitempty' "$AW4/internal/command/retro.go"
echo "PASS: omitempty tags confirmed in retro.go"

# Verify the ordering: SprintRetroKPIs call line number is LESS than measureCause loop line number
kpi_line=$(grep -n 'SprintRetroKPIs' "$AW4/internal/command/improvements.go" | head -1 | cut -d: -f1)
measure_line=$(grep -n 'measureCause' "$AW4/internal/command/improvements.go" | head -1 | cut -d: -f1)
[ -n "$kpi_line" ]
[ -n "$measure_line" ]
echo "SprintRetroKPIs at line $kpi_line, measureCause at line $measure_line"
[ "$kpi_line" -lt "$measure_line" ]
echo "PASS: full KPI compute precedes metric-path validation (the ordering problem is real)"
```
