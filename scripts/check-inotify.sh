#!/usr/bin/env sh
# 检查当前用户的 inotify 实例是否接近上限。
# 退出码 0 = 充足；1 = 已耗尽或剩余不足 8 个（文件监听会报 EMFILE），此时应改用轮询或提高上限。
# 只统计当前用户可读的进程，结果是下限估计。
limit=$(cat /proc/sys/fs/inotify/max_user_instances 2>/dev/null || echo 0)
[ "$limit" -gt 0 ] || exit 0
used=$(find /proc/[0-9]*/fd -maxdepth 1 -lname 'anon_inode:inotify' 2>/dev/null | wc -l)
if [ $((limit - used)) -lt 8 ]; then
  echo "⚠️  inotify 实例已用 ${used}/${limit}，文件监听会报 EMFILE，本次改用轮询模式（略增 CPU 占用）。" >&2
  echo "   根治（需要 sudo，永久生效）：" >&2
  printf '%s\n' "   printf 'fs.inotify.max_user_instances=1024\\nfs.inotify.max_user_watches=524288\\n' | sudo tee /etc/sysctl.d/60-inotify.conf && sudo sysctl --system" >&2
  exit 1
fi
exit 0
