### Run `act_runner` in a Docker Container

```sh
docker run -e GITEA_INSTANCE_URL=http://192.168.8.18:3000 -e GITEA_RUNNER_REGISTRATION_TOKEN=<runner_token> -v /var/run/docker.sock:/var/run/docker.sock -v $PWD/data:/data --name my_runner gitea/act_runner:nightly
```

The `/data` directory inside the docker container contains the runner API keys after registration.
It must be persisted, otherwise the runner would try to register again, using the same, now defunct registration token.
