FROM golang:1.23.2

RUN mkdir /opt/go && \
    apt update && \
    apt install -y libssl-dev
EXPOSE 7081
WORKDIR /opt/go

CMD [ "go", "run", "."]