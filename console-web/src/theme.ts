import { createTheme, Drawer, Modal, type MantineColorsTuple } from '@mantine/core';

/*
 * Design direction: same "Canopy" look as auth-app, in Operate mode throughout.
 *
 * "Canopy": a quiet forest floor seen through frosted glass. The page sits on a
 * still background of soft green/teal light (blurred leaf shapes plus a faint grain),
 * and every surface that carries text is a translucent glass panel dense enough to
 * keep body text at WCAG AA. Brand element is Wood, so the one accent is a deep
 * leaf green. Red is reserved for errors and destructive actions.
 *
 * Palette tokens
 *   bg      light #eef4f0  dark #0b1411   page background under the blobs
 *   ink     light #12201a  dark #e3ece7   body text
 *   muted   light #4b5d54  dark #9db3a8   secondary text (>= 5.5:1 on glass)
 *   rule    light #cfdcd5  dark #22332c   1px dividers
 *   glass   light rgba(255,255,255,.72)  dark rgba(14,26,21,.72), blur 18px saturate 140%
 *   accent  `canopy`, shade 7 #0b7041: links, primary buttons, active nav.
 *           White on shade 7 is ~6.2:1. Dark-scheme links use shade 3 #6ee0a0 (~10:1).
 *
 * Type: Plus Jakarta Sans Variable (UI; has a Vietnamese subset), JetBrains Mono Variable (code and key values only).
 *   h1 2.25 / h2 1.5 / h3 1.2 / body 1.0625 (17px).
 * Radius: 10px panels, 8px controls.
 */

export const canopy: MantineColorsTuple = [
  '#ecfdf3', '#d1fadf', '#a6f0c3', '#6ee0a0', '#36c87a', '#16a35f', '#0f8a4f', '#0b7041', '#095a36', '#06402a',
];

export const sansFont = '"Plus Jakarta Sans Variable", system-ui, -apple-system, "Segoe UI", sans-serif';
export const monoFont = '"JetBrains Mono Variable", ui-monospace, SFMono-Regular, Menlo, Consolas, monospace';

export const theme = createTheme({
  primaryColor: 'canopy',
  primaryShade: { light: 7, dark: 7 },
  colors: { canopy },
  fontFamily: sansFont,
  fontFamilyMonospace: monoFont,
  defaultRadius: 'md',
  radius: { xs: '4px', sm: '6px', md: '8px', lg: '10px', xl: '16px' },
  spacing: { xs: '4px', sm: '8px', md: '16px', lg: '24px', xl: '40px' },
  headings: {
    fontFamily: sansFont,
    fontWeight: '650',
    sizes: {
      h1: { fontSize: '2.25rem', lineHeight: '1.15' },
      h2: { fontSize: '1.5rem', lineHeight: '1.25' },
      h3: { fontSize: '1.2rem', lineHeight: '1.35' },
    },
  },
  fontSizes: { xs: '0.8125rem', sm: '0.875rem', md: '1rem', lg: '1.0625rem', xl: '1.25rem' },
  lineHeights: { md: '1.6' },
  components: {
    // react-remove-scroll injects a <style> element; the strict CSP forbids it.
    Modal: Modal.extend({ defaultProps: { lockScroll: false } }),
    Drawer: Drawer.extend({ defaultProps: { lockScroll: false } }),
  },
});

export const cssVariablesResolver = () => ({
  variables: {},
  light: {
    '--mantine-color-body': '#eef4f0',
    '--mantine-color-text': '#12201a',
    '--mantine-color-dimmed': '#4b5d54',
    '--mantine-color-default-border': '#cfdcd5',
    '--mantine-color-anchor': canopy[7],
  },
  dark: {
    '--mantine-color-body': '#0b1411',
    '--mantine-color-text': '#e3ece7',
    '--mantine-color-dimmed': '#9db3a8',
    '--mantine-color-default-border': '#22332c',
    '--mantine-color-anchor': canopy[3],
  },
});
