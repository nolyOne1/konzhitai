#!/bin/sh
set -eu

data_dir="${YUNLING_OPS_DATA_DIR:-/data}"
if [ "$(id -u)" -ne 0 ]; then
    echo "请使用 root 初始化运维数据卷" >&2
    exit 1
fi
case "$data_dir" in
    /*) ;;
    *) echo "运维数据卷路径必须是绝对路径" >&2; exit 1 ;;
esac
if [ ! -d "$data_dir" ] || [ -L "$data_dir" ]; then
    echo "运维数据卷路径必须是现有实体目录" >&2
    exit 1
fi

chown 10001:10001 "$data_dir"
chmod 0700 "$data_dir"
echo "运维数据卷权限已就绪"
