### Running `act_runner` using `docker-compose`

```yml
...
  gitea:
    image: gitea/gitea
    ...
    healthcheck:
      # checks availability of Gitea's front-end with curl
      test: ["CMD", "curl", "-f", "<instance_url>"]
      interval: 10s
      retries: 3
      start_period: 30s
      timeout: 10s
    environment:
      # GITEA_RUNNER_REGISTRATION_TOKEN can be used to set a global runner registration token.
      # The Gitea version must be v1.23 or higher.
      # It's also possible to use GITEA_RUNNER_REGISTRATION_TOKEN_FILE to pass the location.
      # - GITEA_RUNNER_REGISTRATION_TOKEN=<user-defined registration token>

  runner:
    image: gitea/act_runner
    restart: always
    depends_on:
      gitea:
        # required so runner can attach to gitea, see "healthcheck"
        condition: service_healthy 
        restart: true
    volumes:
      - ./data/act_runner:/data
      - /var/run/docker.sock:/var/run/docker.sock
    environment:
      - GITEA_INSTANCE_URL=<instance url>
      # When using Docker Secrets, it's also possible to use
      # GITEA_RUNNER_REGISTRATION_TOKEN_FILE to pass the location.
      # The env var takes precedence.
      # Needed only for the first start.
      - GITEA_RUNNER_REGISTRATION_TOKEN=<registration token>
```

### Running `act_runner` using Docker-in-Docker (DIND) 

```yml
...
  runner:
    image: gitea/act_runner:latest-dind-rootless
    restart: always
    privileged: true
    depends_on:
      - gitea
    volumes:
      - ./data/act_runner:/data
    environment:
      - GITEA_INSTANCE_URL=<instance url>
      - DOCKER_HOST=unix:///var/run/user/1000/docker.sock
      # When using Docker Secrets, it's also possible to use
      # GITEA_RUNNER_REGISTRATION_TOKEN_FILE to pass the location.
      # The env var takes precedence.
      # Needed only for the first start.
      - GITEA_RUNNER_REGISTRATION_TOKEN=<registration token>
```
