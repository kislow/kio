# Records docs/demo/demo.gif from the scripted world in setup.sh. Run it with
# `make demo`, which builds ki into a throwaway directory and passes it in DEMO_DIR.
Output docs/demo/demo.gif
Output docs/demo/demo.txt

Set Shell "bash"
Set Width 1280
Set Height 330
Set FontSize 14
Set Padding 20
Set Theme "Catppuccin Mocha"
Set TypingSpeed 30ms

Hide
Type `source docs/demo/setup.sh "$DEMO_DIR" && PS1='$ ' && clear`
Enter
Sleep 1s
Show

Type `ki delegate --task "fix the flaky retry test" --project webapp`
Sleep 500ms
Enter
Sleep 11s

Type "ki delegate ls"
Sleep 500ms
Enter
Sleep 3s
