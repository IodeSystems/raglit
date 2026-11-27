# Test plan: st_1m8d0apt8a6pz4zbwpxvp3

## 15.2-min overrun composition: arithmetic check

The prior session (s_1m8ddkkfe7g4p5b8vpywjk) found that the 15.2-min "draft-review overrun" reported in sprints 228, 233, and 234 is the subtask `st_1m7qgbn0949r2b5t9j3t4z`'s **lifetime LLM+tool compute total**, not a measured per-sprint phase:

- LLM compute: 880,730 ms
- Tool compute: 32,243 ms
- Total: 912,973 ms ≈ **15.216 min**

This value is identical across all three sprints because it is the subtask's cumulative compute since creation, not a per-sprint measurement. The subtask has `phases:[]` and `activeMin:0` in the sprint KPIs — no draft-review phase was measured in any of the three sprints.

**Conclusion:** the 15.2 min is blocked-wait/lifetime-compute misattributed as a per-sprint phase budget overrun. No actual draft-review work of 15+ minutes exists. The fix is aw4-side (do not re-dispatch blocked subtasks for review, or exclude blocked-wait from the review-phase budget), not a raglit-side draft split.

```sh
set -e
python3 -c "
total = 880730 + 32243
minutes = total / 60000
print(f'Total: {total} ms = {minutes:.3f} min')
assert abs(minutes - 15.2) < 0.1, f'{minutes} not ~15.2'
print('OK: 912973 ms rounds to 15.2 min')
"
```
