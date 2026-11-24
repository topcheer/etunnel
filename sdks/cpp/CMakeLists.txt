cmake_minimum_required(VERSION 3.15)
project(etunnel LANGUAGES CXX)

set(CMAKE_CXX_STANDARD 17)
set(CMAKE_CXX_STANDARD_REQUIRED ON)

find_package(Threads REQUIRED)
# asio 1.13 - the only standalone asio websocketpp 0.8.2 accepts, see below -
# reads ASN1_STRING fields directly, which OpenSSL 4 made opaque. Homebrew's
# `openssl` is 4.x now, so prefer an installed OpenSSL 3.x keg unless the user
# pointed OPENSSL_ROOT_DIR somewhere (same pin as sdks/cpp/tests/*.sh).
if(NOT DEFINED OPENSSL_ROOT_DIR AND EXISTS "/opt/homebrew/opt/openssl@3")
    set(OPENSSL_ROOT_DIR "/opt/homebrew/opt/openssl@3" CACHE PATH "OpenSSL root (asio 1.13 needs OpenSSL 3.x)")
    message(STATUS "using OpenSSL 3.x keg ${OPENSSL_ROOT_DIR} (asio 1.13 does not build against OpenSSL 4)")
endif()
find_package(OpenSSL REQUIRED)
find_package(CURL REQUIRED)

# websocketpp: header-only, ships no CMake config on Homebrew/Debian
find_path(WEBSOCKETPP_INCLUDE_DIR websocketpp/client.hpp
          HINTS /opt/homebrew/opt/websocketpp/include /usr/local/opt/websocketpp/include)
if(NOT WEBSOCKETPP_INCLUDE_DIR)
  message(FATAL_ERROR "websocketpp not found (Debian: libwebsocketpp-dev; brew: websocketpp)")
endif()

# Boost.Asio preferred (websocketpp's primary backend); fall back to
# standalone asio where Boost is unavailable (e.g. Debian + libasio-dev) or
# too new - Boost 1.87 removed asio::io_service, which websocketpp 0.8.x
# still references (#49).
find_package(Boost 1.66 QUIET)
# FindBoost's Boost_VERSION is numeric (109200) on modern CMake but dotted
# ("1.92.0") on some others; normalize to dotted for the comparison.
if(Boost_FOUND)
    if(Boost_VERSION MATCHES "^[0-9]+$")
        string(REGEX REPLACE "^([0-9])([0-9][0-9])([0-9][0-9])$" "\\1.\\2.\\3"
               BOOST_ASIO_VER "${Boost_VERSION}")
    else()
        set(BOOST_ASIO_VER "${Boost_VERSION}")
    endif()
endif()
if(Boost_FOUND AND BOOST_ASIO_VER VERSION_LESS 1.87)
    set(ASIO_INCLUDE_DIR ${Boost_INCLUDE_DIRS})
    set(ASIO_DEFINITIONS )
    message(STATUS "using Boost.Asio ${Boost_VERSION}")
else()
    # ASIO_DIR is the name the test scripts (sdks/cpp/tests/*.sh) use for the
    # include path; accept it as an alias of the canonical ASIO_INCLUDE_DIR so
    # following either name works. Drop a stale cached ASIO_INCLUDE_DIR from an
    # earlier configure so the alias is really honoured.
    if(ASIO_DIR)
        unset(ASIO_INCLUDE_DIR CACHE)
        set(ASIO_INCLUDE_DIR "${ASIO_DIR}" CACHE PATH "standalone asio include directory")
    endif()
    # websocketpp 0.8.2 (brew websocketpp 0.8.2, Debian libwebsocketpp-dev) needs
    # the pre-1.14 asio API (asio::io_service, removed in asio 1.14 and in
    # Boost.Asio 1.87): standalone asio <= 1.13.3, i.e. ASIO_VERSION <= 101303.
    # sdks/cpp/tests pin asio-1-13-0 (ASIO_VERSION 101300) and fetch it on
    # first use; brew's standalone asio (1.38+) is too new and is rejected
    # below with an actionable message instead of a wall of header errors.
    find_path(ASIO_INCLUDE_DIR asio.hpp
              HINTS /opt/homebrew/opt/asio/include /usr/local/opt/asio/include)
    if(ASIO_INCLUDE_DIR)
        file(READ "${ASIO_INCLUDE_DIR}/asio/version.hpp" ASIO_VERSION_HDR)
        string(REGEX MATCH "define ASIO_VERSION ([0-9]+)" _ "${ASIO_VERSION_HDR}")
        set(ASIO_VER_NUM "${CMAKE_MATCH_1}")
        if(NOT ASIO_VER_NUM STREQUAL "" AND ASIO_VER_NUM GREATER 101303)
            # asio/version.hpp encoding: /100000 major, /100 %1000 minor, %100 patch
            math(EXPR ASIO_VER_MAJOR "${ASIO_VER_NUM} / 100000" OUTPUT_FORMAT DECIMAL)
            math(EXPR ASIO_VER_MINOR "(${ASIO_VER_NUM} / 100) % 1000" OUTPUT_FORMAT DECIMAL)
            math(EXPR ASIO_VER_PATCH "${ASIO_VER_NUM} % 100" OUTPUT_FORMAT DECIMAL)
            message(FATAL_ERROR "standalone asio at ${ASIO_INCLUDE_DIR} is ${ASIO_VER_MAJOR}.${ASIO_VER_MINOR}.${ASIO_VER_PATCH}, too new for websocketpp 0.8.2 (needs <= 1.13.3, ASIO_VERSION 101303). sdks/cpp/tests fetch asio 1.13 automatically, or point CMake at an older copy: cmake -DASIO_INCLUDE_DIR=/path/to/asio-1.13/include (ASIO_DIR is accepted as an alias)")
        endif()
        set(ASIO_DEFINITIONS ASIO_STANDALONE)
        message(STATUS "using standalone Asio ${ASIO_VER_NUM} (${ASIO_INCLUDE_DIR})")
    else()
        message(FATAL_ERROR "neither Boost.Asio (<1.87) nor standalone asio found; set ASIO_INCLUDE_DIR (or ASIO_DIR) to an asio <= 1.13.3 include directory")
    endif()
endif()

add_executable(etunnel etunnel.cpp)
if(ASIO_DEFINITIONS)
  target_compile_definitions(etunnel PRIVATE ${ASIO_DEFINITIONS})
endif()
target_include_directories(etunnel PRIVATE ${WEBSOCKETPP_INCLUDE_DIR} ${ASIO_INCLUDE_DIR})
target_link_libraries(etunnel PRIVATE CURL::libcurl OpenSSL::SSL OpenSSL::Crypto Threads::Threads)
