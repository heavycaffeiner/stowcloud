---
name: plan-in-prose-not-todo
description: "Multi-step work must be tracked with the todo tool, not narrated as a numbered plan in prose"
condition: ["(?i)(?:계획|플랜|순서|절차|단계|방안|접근|작업\\s*순서|구현\\s*순서|진행\\s*순서|plan|steps?|approach|outline|roadmap|sequence|procedure|order of work)\\s*[:：]\\s*\\n?\\s*(?:1[.)]|첫)", "(?:^|\\n)\\s*1[.)]\\s[^\\n]{8,}\\n\\s*2[.)]\\s[^\\n]{8,}", "\\b1[.)]\\s+\\S[^\\n]{6,}?\\s+2[.)]\\s+\\S", "(?:첫째|첫\\s*번째)[^\\n]{0,80}(?:둘째|두\\s*번째)", "(?:^|\\n)\\s*[-*]\\s*\\[\\s\\]\\s+\\S"]
scope: "text"
---

Execute multi-step work under the todo tool, not as a numbered plan narrated in prose. Before starting: `todo init` with one item per concrete step. While working: mark `start` when a step begins and `done` the moment it completes, batching each call with real work. The user follows and steers through the todo list; a plan written into prose is invisible to that and is the behavior being corrected here.
