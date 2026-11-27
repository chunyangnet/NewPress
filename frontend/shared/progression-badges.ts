document.querySelectorAll<HTMLElement>('[data-progression-color]').forEach(badge => {
  const color = badge.dataset.progressionColor;
  if (color && /^#[0-9a-f]{6}$/.test(color)) badge.style.setProperty('--progression-accent', color);
});
