## Using Rootless Docker with`act_runner`

Here is a simple example of how to set up `act_runner` with rootless Docker. It has been created with Debian, but other Linux should work the same way.

Note: This procedure needs a real login shell -- using `sudo su` or other method of accessing the account will fail some of the steps below.

As `root`:

- Create a user to run both `docker` and `act_runner`. In this example, we use a non-privileged account called `rootless`.

```bash
 useradd -m rootless
 passwd rootless
 apt-get install -y uidmap # Not mentioned but needed for docker rootless.
 ```

- Install [`docker-ce`](https://docs.docker.com/engine/install/)
- (Recommended) Disable the system-wide Docker daemon

     ``systemctl disable --now docker.service docker.socket``

As the `rootless` user:

- Follow the instructions for [enabling rootless mode](https://docs.docker.com/engine/security/rootless/)
- Add the following line to the `/home/rootless/.bashrc`:

```bash
for f in ./.bashrc.d/*.bash; do echo "Processing $f file..."; . "$f"; done
```

- Create the .bashrc.d directory `mkdir ~/.bashrc.d`
- Add the following lines to the `/home/rootless/.bashrc.d/rootless-docker.bash`:

```bash
export XDG_RUNTIME_DIR=/home/rootless/.docker/run
export PATH=/home/rootless/bin:$PATH
export DOCKER_HOST=unix:///run/user/$(id -u)/docker.sock
```

- Reboot. Ensure that the Docker process is working.
- Create a directory for saving `act_runner` data between restarts

 `mkdir /home/rootless/act_runner`

- Register the runner from the data directory

```bash
 cd /home/rootless/act_runner
 act_runner register
```

- Generate a `act_runner` configuration file in the data directory. Edit the file to adjust for the system.

```bash
 act_runner generate-config >/home/rootless/act_runner/config
```

- Create a new user-level`systemd` unit file as `/home/rootless/.config/systemd/user/act_runner.service` with the following contents:

```bash
 Description=Gitea Actions runner
 Documentation=https://gitea.com/gitea/act_runner
 After=docker.service

 [Service]
 Environment=PATH=/home/rootless/bin:/sbin:/usr/sbin:/home/rootless/bin:/home/rootless/bin:/home/rootless/bin:/usr/local/bin:/usr/bin:/bin:/usr/local/games:/usr/games
 Environment=DOCKER_HOST=unix:///run/user/1001/docker.sock
 ExecStart=/usr/bin/act_runner daemon -c /home/rootless/act_runner/config
 ExecReload=/bin/kill -s HUP $MAINPID
 WorkingDirectory=/home/rootless/act_runner
 TimeoutSec=0
 RestartSec=2
 Restart=always
 StartLimitBurst=3
 StartLimitInterval=60s
 LimitNOFILE=infinity
 LimitNPROC=infinity
 LimitCORE=infinity
 TasksMax=infinity
 Delegate=yes
 Type=notify
 NotifyAccess=all
 KillMode=mixed

 [Install]
 WantedBy=default.target
```

- Reboot

After the system restarts, check that the`act_runner` is working and that the runner is connected to Gitea.

````bash
 systemctl --user status act_runner
 journalctl --user -xeu act_runner
