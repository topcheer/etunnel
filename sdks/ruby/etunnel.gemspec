# frozen_string_literal: true

Gem::Specification.new do |s|
  s.name = 'etunnel'
  s.version = '1.0.1'
  s.summary = 'Expose a local service through an etunnel reverse-tunnel server with one line of code'
  s.description = 'Expose a local service through an etunnel reverse-tunnel server with one line of code. '
                  'Buffer-based protocol client using faye-websocket.'
  s.authors = ['etunnel authors']
  s.license = 'MIT'
  s.homepage = 'https://github.com/ggcode/etunnel'
  s.required_ruby_version = '>= 2.7'

  s.files = ['lib/etunnel.rb', 'bin/etunnel', 'README.md', 'LICENSE']
  s.require_paths = ['lib']
  s.bindir = 'bin'
  s.executables = ['etunnel']

  s.add_runtime_dependency 'faye-websocket', '~> 0.11'
  s.metadata['source_code_uri'] = 'https://github.com/ggcode/etunnel'
end
