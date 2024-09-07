FROM civitaspo/ruby-awscli

RUN mkdir -p /usr/src/app
WORKDIR /usr/src/app
COPY ./parser.rb .
COPY ./run .
CMD ["./run"]
ENTRYPOINT []
