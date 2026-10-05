// VRISE foundation: all custom styles and Vuetify components share these values.
export const palette = Object.freeze({
  copper: '#B8865A', gold: '#C9A96E', bronze: '#8B6240',
  ivory: '#FAF6F0', champagne: '#F0E6D3', linen: '#DDD0BE',
  espresso: '#1C1410', charcoal: '#2D2420', umber: '#4A3728',
  blush: '#EDD5C0', rose: '#D4A898', stone: '#C8BFB5',
});
export const vriseTheme = {
  dark: false,
  colors: {
    ...palette,
    primary: palette.copper, secondary: palette.bronze,
    background: palette.ivory, surface: palette.ivory,
    'surface-bright': palette.ivory, 'surface-light': palette.champagne,
    'surface-variant': palette.champagne, 'on-surface-variant': palette.umber,
    'on-background': palette.espresso, 'on-surface': palette.espresso,
    'on-primary': palette.espresso, 'on-secondary': palette.ivory,
    success: palette.bronze, warning: palette.gold,
    error: palette.charcoal, info: palette.bronze,
    'on-success': palette.ivory, 'on-warning': palette.espresso,
    'on-error': palette.ivory, 'on-info': palette.ivory,
  },
  variables: { 'border-color': palette.linen, 'border-opacity': 1 },
};
export const themeOptions = { defaultTheme: 'vrise', themes: { vrise: vriseTheme } };
export function applyPalette(element) {
  for (const [name, value] of Object.entries(palette)) element.style.setProperty(`--vrise-${name}`, value);
}
