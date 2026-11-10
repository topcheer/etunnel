# frozen_string_literal: true

Gem::Specification.new do |s|
  s.name = 'etunnel'
  s.version = '1.4.0'
  summary = 'Expose local services to the internet in one line of code: instant public HTTPS URLs with automatic TLS, through NAT and firewalls, no signup.'
  s.summary = summary
  s.description = "#{summary} Buffer-based protocol client using faye-websocket."
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
  s.add_development_dependency "rake", "~> 13.0"
end
