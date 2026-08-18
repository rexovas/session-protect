# VHS recording script — render with: vhs scripts/demo/demo.tape
# Requires scripts/demo/setup.sh to have run first.
Output assets/demo.gif

Set FontSize 14
Set Width 1200
Set Height 700
Set Padding 16
Set TypingSpeed 70ms

Hide
Type "source scripts/demo/env.sh && clear"
Enter
Show

Type "sp browse"
Sleep 800ms
Enter
Sleep 2500ms

# Into a project: session list with live states
Enter
Sleep 2000ms
Down
Sleep 600ms
Down
Sleep 800ms

# Inspector: overview → usage/cost → transcript
Type "i"
Sleep 3000ms
Tab
Sleep 2500ms
Tab
Sleep 3000ms
Escape
Sleep 800ms

# Instant filter
Type "/"
Sleep 400ms
Type "auth"
Sleep 2000ms
Escape
Sleep 600ms

# Back up; all sessions; reveal the lost one
Escape
Sleep 800ms
Ctrl+a
Sleep 2000ms
Type "x"
Sleep 2200ms
Type "x"
Sleep 500ms

# Restore dialog on the recoverable session
Escape
Sleep 600ms
Down
Sleep 600ms
Enter
Sleep 1200ms
Type "r"
Sleep 2500ms
Escape
Sleep 600ms

# Stats + protection summary
Type "m"
Sleep 2500ms
Escape
Sleep 800ms

Type "q"
Sleep 1000ms
