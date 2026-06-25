---
description: Commit current changes to git
agent: build
---
Commit the current changes to git.
Prefix git commits with your agent's name, e.g. "OpenCode: The change". Use short commit messages. Explain details in body.

Procedure:
1. Run `git status` and `git diff --stat` to show the user what changed.
2. If files outside the current task scope are modified, ask the user whether to include or omit each.
3. Run `git log --oneline -5` to check the repo's commit style.
4. Draft a summary line and a body with only short, relevant information — no long lists or unnecessary detail.
5. Present the summary and body in a regular message so the user can read them.
6. If an "ask" tool is available, use it to confirm each of:
   - The list of committed/omitted files is approved
   - The proposed summary line is approved
   - The proposed commit body is approved
7. If the user wants changes, repeat from step 4 and confirm again.
8. Stage the approved files and commit.
