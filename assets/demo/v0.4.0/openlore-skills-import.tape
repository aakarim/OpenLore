Output assets/demo/v0.4.0/openlore-skills-import.mp4

Set Shell "bash"
Set Width 1920
Set Height 1080
Set FontSize 22
Set LineHeight 1.25
Set TypingSpeed 6ms
Set Theme { "name": "Oiya", "black": "#272036", "red": "#FF9A68", "green": "#E7E75F", "yellow": "#FF9A68", "blue": "#E0D1FF", "magenta": "#F07FFF", "cyan": "#C9C1D6", "white": "#F3F0E9", "brightBlack": "#6E6383", "brightRed": "#FFB088", "brightGreen": "#FFFF8A", "brightYellow": "#FFB088", "brightBlue": "#EEE6FF", "brightMagenta": "#F5AAFF", "brightCyan": "#E0D1FF", "brightWhite": "#FFFFFF", "background": "#1A1526", "foreground": "#F3F0E9", "selection": "#3E3453", "cursor": "#FF9A68" }
Set Padding 24
Set Margin 0
Set MarginFill "#1A1526"

Hide
Type "./assets/demo/v0.4.0/openlore-skills-import-prepare.sh"
Enter
Wait+Screen /video-session-ready/
Type "tmux -L openlore-skills-video attach -t demo"
Enter
Sleep 1s
Show

Type@50ms "# Teach your agent to set up OpenLore"
Enter
Type@12ms "ssh openlore.sh teach | claude"
Enter
Wait+Screen /Ready: openlore/
Sleep 700ms

Type@50ms "# Start a fresh OpenLore server"
Enter
Type@24ms "openlore ./knowledge"
Enter
Wait+Screen /OpenLore ready/
Sleep 1s

Ctrl+B
Right
Hide
Type@0ms "user-prompt"
Enter
Sleep 100ms
Show
Sleep 2500ms

Type `ssh skills.local 'skills enable "$HOME"'`
Enter
Wait+Screen /"status":"enabled"/
Sleep 700ms

Type `ssh skills.local 'skills import mattpocock/skills/skills/productivity/grill-me@main "$HOME"'`
Enter
Wait+Screen /imported/
Sleep 1s

Type `ssh skills.local 'skills status "$HOME" | grep remote'`
Enter
Wait+Screen /tracking/
Sleep 1100ms

Type `ssh skills.local 'skills update "$HOME/grill-me"'`
Enter
Wait+Screen /current/
Sleep 1s

Ctrl+L
Hide
Type@0ms "use-skill-prompt"
Enter
Sleep 100ms
Show
Sleep 2200ms

Hide
Type@0ms "agent-use"
Enter
Sleep 100ms
Show
Type `ssh skills.local 'skills status "$HOME" | grep grill-me'`
Enter
Wait+Screen /grill-me/
Sleep 800ms

Type `ssh skills.local 'tail -3 "$HOME/grill-me/SKILL.md"'`
Enter
Wait+Screen /grilling/
Sleep 900ms

Hide
Type@0ms "agent-grill"
Enter
Sleep 100ms
Show
Sleep 2500ms

Hide
Type@0ms "demo-success"
Enter
Sleep 100ms
Show
Sleep 1800ms
