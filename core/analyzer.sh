#   ./log_analyzer.sh "3 days ago" # Scans the last 3 days
#
# ==============================================================================

# Set the time range for the log scan. Use the first command-line argument
# if it's provided, otherwise default to "12 hours ago".
TIME_RANGE=${1:-"12 hours ago"}

echo "🔍 Scanning 'bor' service logs from the last '$TIME_RANGE'..."
echo "📊 Generating histogram for single-block import times ('blocks=1')..."
echo "📊 Extracting segment averages (execavg vs statecalcavg vs validation) if present..."
echo ""

# --- Main Logic ---
# The main pipeline is wrapped in a command group `{ ... }` to fix the sorting.
# 1. The `echo` commands run first, printing a static header for the output.
# 2. `journalctl`: Fetches the logs for the 'bor' unit.
# 3. `awk`: Processes the log lines.
#    - It collects data into the `histogram` array just as before.
#    - The `END` block is now simplified: it *only* prints the data rows.
#      The header printing has been moved outside the awk script.
# 4. `sort -n`: This now receives only the data rows, so it sorts them
#    numerically without misplacing the header.

{
  # Print the header first and separately.
  echo "Time Range     Count"
  echo "--------------------"

  # Process the logs and print only the data.
  journalctl -u bor --since "$TIME_RANGE" --no-pager | \
    awk '
      /Imported/ && /blocks=1 / {
        # This regex now captures both the number and the unit (s or ms)
        if (match($0, /elapsed=([0-9.]+)(ms|s)/, m)) {
          elapsed_val = m[1] # The numerical value
          unit = m[2]        # The unit ("ms" or "s")

          # If the unit is milliseconds, convert it to seconds
          if (unit == "ms") {
            elapsed_val = elapsed_val / 1000
          }

          # Determine the correct bucket and increment the count
          bucket = int(elapsed_val)
          histogram[bucket]++
        }
      }
      END {
        # This block now ONLY prints the histogram data, not the header.
        for (bucket in histogram) {
          printf "%-12s %d\n", sprintf("%d-%ds:", bucket, bucket + 1), histogram[bucket]
        }
      }
    ' | \
    sort -n
}

# Extract per-block timing breakdowns from BlockTiming logs
echo ""
echo "EndBlock   Blocks  Exec(ms)  StateCalc(ms)  Validation(ms)  Elapsed(s)"
echo "------------------------------------------------------------------"
journalctl -u bor --since "$TIME_RANGE" --no-pager | \
  grep "Imported" | \
  awk '
    {
      endblk=""; blocks=""; execdur=""; statedur=""; elapsed=""
      for (i=1; i<=NF; i++) {
        if ($i ~ /number=/)      { split($i,a,"="); endblk=a[2] }
        else if ($i ~ /blocks=/) { split($i,a,"="); blocks=a[2] }
        else if ($i ~ /exec=/) { sub(/exec=/, "", $i); execdur=$i }
        else if ($i ~ /statecalc=/) { sub(/statecalc=/, "", $i); statedur=$i }
        else if ($i ~ /elapsed=/) { sub(/elapsed=/, "", $i); elapsed=$i }
      }
      fnorm_ms = function(x) {
        # Normalize PrettyDuration to ms (supports ms, s, and us/µs)
        if (x ~ /ms$/) { sub(/ms$/, "", x); return x+0 }
        if (x ~ /µs$/) { sub(/µs$/, "", x); return (x+0)/1000 }
        if (x ~ /us$/) { sub(/us$/, "", x); return (x+0)/1000 }
        if (x ~ /s$/)  { sub(/s$/,  "", x); return (x+0)*1000 }
        return x+0
      }
      fnorm_s = function(x) {
        if (x ~ /ms$/) { sub(/ms$/, "", x); return (x+0)/1000 }
        if (x ~ /µs$/) { sub(/µs$/, "", x); return (x+0)/1e6 }
        if (x ~ /us$/) { sub(/us$/, "", x); return (x+0)/1e6 }
        if (x ~ /s$/)  { sub(/s$/,  "", x); return (x+0) }
        return x+0
      }
      if (endblk!="" && blocks!="") {
        e = (execdur!="") ? fnorm_ms(execdur) : 0
        s = (statedur!="") ? fnorm_ms(statedur) : 0
        el = (elapsed!="") ? fnorm_s(elapsed) : 0
        v = (el>0) ? (el*1000 - e - s) : 0
        printf "%8s %7s %9.2f %13.2f %14.2f %11.2f\n", endblk, blocks, e, s, v, el
      }
    }
  '

echo ""
echo "✅ Analysis complete."

