cmake_minimum_required(VERSION 3.15)
project(etunnel LANGUAGES CXX)

set(CMAKE_CXX_STANDARD 17)
set(CMAKE_CXX_STANDARD_REQUIRED ON)

find_package(Threads REQUIRED)
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
    find_path(ASIO_INCLUDE_DIR asio.hpp
              HINTS /opt/homebrew/opt/asio/include /usr/local/opt/asio/include)
    if(ASIO_INCLUDE_DIR)
        # websocketpp 0.8.x needs the pre-1.14 asio API (io_service); brew's
        # standalone asio is newer, so check and explain instead of spewing
        # header errors. The SDK tests pin asio 1.13 automatically.
        file(READ "${ASIO_INCLUDE_DIR}/asio/version.hpp" ASIO_VERSION_HDR)
        string(REGEX MATCH "define ASIO_VERSION ([0-9]+)" _ "${ASIO_VERSION_HDR}")
        set(ASIO_VER_NUM "${CMAKE_MATCH_1}")
        if(NOT ASIO_VER_NUM STREQUAL "" AND ASIO_VER_NUM GREATER 101303)
            message(FATAL_ERROR "standalone asio at ${ASIO_INCLUDE_DIR} is too new for websocketpp 0.8.x (needs <= 1.13); sdks/cpp/tests fetch asio 1.13 automatically, or set ASIO_DIR to an older copy")
        endif()
        set(ASIO_DEFINITIONS ASIO_STANDALONE)
        message(STATUS "using standalone Asio (${ASIO_INCLUDE_DIR})")
    else()
        message(FATAL_ERROR "neither Boost.Asio (<1.87) nor standalone asio found")
    endif()
endif()

add_executable(etunnel etunnel.cpp)
if(ASIO_DEFINITIONS)
  target_compile_definitions(etunnel PRIVATE ${ASIO_DEFINITIONS})
endif()
target_include_directories(etunnel PRIVATE ${WEBSOCKETPP_INCLUDE_DIR} ${ASIO_INCLUDE_DIR})
target_link_libraries(etunnel PRIVATE CURL::libcurl OpenSSL::SSL OpenSSL::Crypto Threads::Threads)
