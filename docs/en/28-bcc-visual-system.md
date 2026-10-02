# BAFT BCC Visual System

This document defines the visual identity used by BAFT and is derived from the current BCC interface.

## Core palette

```text
Background        #090b10
Radial surface    #182238
Card              #111722
Input / inset     #0b1019
Border            #263147
Primary text      #eef1f6
Muted text        #8e9aaf
Gold accent       #e8b54a
Healthy           #92f0bf
Healthy surface   #123e2c
Error             #ffabb6
Error surface     #4b2026
Neutral surface   #37333a
```

## Interface principles

- Use a near-black base with a subtle navy radial surface near the top.
- Use independent cards with low-contrast borders and approximately 16px radius.
- Reserve gold for primary actions, highlights, and important signals.
- Primary text is cool white; secondary text is muted blue-gray.
- Green and red are status colors, not decorative colors.
- Keep the UI dense but calm. Gradients and glow are reserved for hero/signal use.
- Persian content is RTL; code, hashes, IPs, commands, and technical identifiers are LTR.

## Reference CSS tokens

```css
:root {
  --bcc-bg: #090b10;
  --bcc-bg-radial: #182238;
  --bcc-card: #111722;
  --bcc-inset: #0b1019;
  --bcc-border: #263147;
  --bcc-text: #eef1f6;
  --bcc-muted: #8e9aaf;
  --bcc-gold: #e8b54a;
  --bcc-success: #92f0bf;
  --bcc-success-bg: #123e2c;
  --bcc-error: #ffabb6;
  --bcc-error-bg: #4b2026;
  --bcc-neutral-bg: #37333a;
  --bcc-radius-card: 16px;
  --bcc-radius-control: 10px;
}
```

## Source

These tokens are extracted from the active stylesheet in `internal/bcc/dashboard.go` and serve as the shared visual reference for BAFT UI and documentation.
