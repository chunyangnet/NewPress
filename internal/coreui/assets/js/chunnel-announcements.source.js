document.querySelectorAll('[data-announcement-delete]').forEach(form => {
  form.addEventListener('submit', event => {
    if (!window.confirm('删除这条公告？此操作会立即从公开列表移除。')) event.preventDefault();
  });
});
