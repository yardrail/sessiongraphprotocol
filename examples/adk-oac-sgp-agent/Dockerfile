FROM golang:1.26 AS build
WORKDIR /src

COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go build -o /out/oac-sgp-agent ./main.go

FROM gcr.io/distroless/base-debian12
WORKDIR /app

LABEL org.openagentcontainers.version="v1alpha2"
LABEL org.openagentcontainers.name="adk-oac-sgp-agent"
LABEL org.openagentcontainers.description="ADK example agent with SGP JSON session persistence"
LABEL org.openagentcontainers.documentation="https://github.com/restrukt-ai/sessiongraphprotocol/tree/main/examples/adk-oac-sgp-agent"
LABEL org.openagentcontainers.source="https://github.com/restrukt-ai/sessiongraphprotocol"
LABEL org.openagentcontainers.licenses="Apache-2.0"
LABEL org.openagentcontainers.orchestrator.env="ORCHESTRATOR_URL"
LABEL org.openagentcontainers.events.agent-events.schema.path="/app/schemas/task-event.schema.json"
LABEL org.openagentcontainers.events.agent-events.schema.mimetype="application/json"
LABEL org.openagentcontainers.inference.api_base.env="OPENAI_BASE_URL"
LABEL org.openagentcontainers.inference.api_key.env="OPENAI_API_KEY"
LABEL org.openagentcontainers.inference.text.models="gemini-3.1-flash-lite"

COPY --from=build /out/oac-sgp-agent /app/oac-sgp-agent
COPY schemas /app/schemas

ENV SGP_SESSION_DIR=/var/lib/sgp-sessions

ENTRYPOINT ["/app/oac-sgp-agent"]
CMD ["console"]
