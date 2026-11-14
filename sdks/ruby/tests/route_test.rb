# frozen_string_literal: true

# Route table tests (same semantics table as sdks/node/route.js).
# Run: ruby tests/route_test.rb
require_relative '../lib/etunnel'

fails = 0
ck = lambda do |name, got, want|
  if got.to_s != want.to_s
    fails += 1
    warn "FAIL #{name}: got #{got.inspect} want #{want.inspect}"
  end
end
msg = lambda { |body|
  begin
    body.call
    'accepted'
  rescue ArgumentError => e
    e.message
  end
}

rs = Etunnel.normalize_routes([
  Etunnel::Route.new('/', 'fe:1'),
  Etunnel::Route.new('/api', 'be:1'),
  Etunnel::Route.new('/api/v2', 'be2:1')
])
ck.call('/apiv2 -> /', Etunnel.route_for(rs, '/apiv2')&.first, 'fe:1')
ck.call('/apiary -> /', Etunnel.route_for(rs, '/apiary')&.first, 'fe:1')
ck.call('/api exact', Etunnel.route_for(rs, '/api')&.first, 'be:1')
ck.call('/api/u', Etunnel.route_for(rs, '/api/u')&.first, 'be:1')
ck.call('longest wins', Etunnel.route_for(rs, '/api/v2/x?q')&.first, 'be2:1')

ss = Etunnel.normalize_routes([
  Etunnel::Route.new('/api', 'be:1', true),
  Etunnel::Route.new('/', 'fe:1', true)
])
ck.call('strip keeps query', Etunnel.route_for(ss, '/api/u?x=1&y=2')&.last, '/u?x=1&y=2')
ck.call('exact strip -> /', Etunnel.route_for(ss, '/api')&.last, '/')
ck.call('strip root prefix = original', Etunnel.route_for(ss, '/app?c=1')&.last, '/app?c=1')

one = Etunnel.normalize_routes([Etunnel::Route.new('/api', 'be:1')])
ck.call('no match -> nil', Etunnel.route_for(one, '/other'), nil)

ck.call('addr required', msg.call(-> { Etunnel.normalize_routes([Etunnel::Route.new('/', '')]) }).include?('addr is required'), true)
ck.call('must start with /', msg.call(-> { Etunnel.normalize_routes([Etunnel::Route.new('api', 'a:1')]) }).include?("must start with"), true)
ck.call('duplicate', msg.call(-> { Etunnel.normalize_routes([Etunnel::Route.new('/api', 'a:1'), Etunnel::Route.new('/api/', 'a:2')]) }).include?('duplicate'), true)

if fails.zero?
  puts 'ruby route tests: PASS'
else
  warn "ruby route tests: FAIL (#{fails})"
  exit 1
end
