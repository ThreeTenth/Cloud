# 通过 readlink 获取绝对路径，再取出目录
work_path=$(dirname $(readlink -f $0))

systemctl stop cloud-api

mv $work_path/cloud-api-amd64-linux-v1 /usr/local/bin/cloud-api
mv $work_path/cloud-api.service /lib/systemd/system

rm -rf $work_path

systemctl daemon-reload
systemctl enable cloud-api

systemctl start cloud-api