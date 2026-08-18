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

# Content search: transcript hits ranked by count
Type "/"
Sleep 300ms
Type "websocket"
Sleep 1000ms
Ctrl+S
Sleep 3200ms
Escape
Sleep 500ms
Escape
Sleep 500ms

# AI find: describe the session you half-remember
Ctrl+G
Sleep 800ms
Type "the one where we fixed socket reconnects"
Sleep 2200ms
Escape
Sleep 600ms

# Back up; all sessions; reveal the lost one
Escape
Sleep 800ms
Ctrl+A
Sleep 2000ms
Type "x"
Sleep 2200ms
Type "x"
Sleep 500ms

# Restore the recoverable session — for real
Escape
Sleep 600ms
Type "G"
Sleep 600ms
Enter
Sleep 1200ms
Type "r"
Sleep 2000ms
Left
Sleep 800ms
Enter
Sleep 2500ms

# Resume it in a new window (dialog, then decline)
Type "o"
Sleep 2500ms
Escape
Sleep 600ms

# Transplant: move the session elsewhere (plan preview, then decline)
Type "t"
Sleep 1200ms
Type "archived-tooling"
Sleep 800ms
Enter
Sleep 2800ms
Escape
Sleep 400ms
Escape
Sleep 600ms

# Stats + protection summary
Type "m"
Sleep 2500ms
Escape
Sleep 800ms

Type "q"
Sleep 1000ms
