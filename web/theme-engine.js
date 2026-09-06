document.addEventListener('DOMContentLoaded', () => {
  const themeToggleBtn = document.getElementById('theme-toggle');
  const lightIcon = document.getElementById('theme-toggle-light-icon');
  const darkIcon = document.getElementById('theme-toggle-dark-icon');

  if (!themeToggleBtn || !lightIcon || !darkIcon) {
    return;
  }

  const savedTheme = localStorage.getItem('portfolio-theme');
  if (savedTheme === 'dark') {
    document.documentElement.classList.add('dark');
  } else {
    document.documentElement.classList.remove('dark');
  }

  function setIconVisible(icon, visible) {
    icon.hidden = !visible;
    icon.style.display = visible ? 'inline-block' : 'none';
    icon.setAttribute('aria-hidden', String(!visible));
  }

  function updateToggleIcons() {
    const isDark = document.documentElement.classList.contains('dark');
    setIconVisible(lightIcon, !isDark);
    setIconVisible(darkIcon, isDark);
  }

  themeToggleBtn.addEventListener('click', () => {
    const isDark = document.documentElement.classList.contains('dark');

    if (isDark) {
      document.documentElement.classList.remove('dark');
      localStorage.setItem('portfolio-theme', 'light');
    } else {
      document.documentElement.classList.add('dark');
      localStorage.setItem('portfolio-theme', 'dark');
    }

    updateToggleIcons();
  });

  updateToggleIcons();
});
