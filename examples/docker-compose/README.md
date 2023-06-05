### Running `act_runner` using `docker-compose`

```yml
...
  gitea:
    image: gitea/gitea
    ...

  runner:
    image: gitea/act_runner
    restart: always
    depends_on:
      - gitea
    volumes:
      - ./data/act_runner:/data
      - /var/run/docker.sock:/var/run/docker.sock
    environment:
      - GITEA_INSTANCE_URL=<instance url>
      - GITEA_RUNNER_REGISTRATION_TOKEN=<registration token>
```
