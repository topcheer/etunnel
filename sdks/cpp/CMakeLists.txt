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

# Boost.Asio preferred (websocketpp's primary backend, works with modern Boost);
# fall back to standalone asio where Boost is unavailable (e.g. Debian + libasio-dev).
find_package(Boost 1.66 QUIET)
if(Boost_FOUND)
  set(ASIO_INCLUDE_DIR ${Boost_INCLUDE_DIRS})
  set(ASIO_DEFINITIONS )
  message(STATUS "using Boost.Asio ${Boost_VERSION}")
else()
  find_path(ASIO_INCLUDE_DIR asio.hpp
            HINTS /opt/homebrew/opt/asio/include /usr/local/opt/asio/include)
  if(ASIO_INCLUDE_DIR)
    set(ASIO_DEFINITIONS ASIO_STANDALONE)
    message(STATUS "using standalone Asio")
  else()
    message(FATAL_ERROR "neither Boost nor standalone asio found")
  endif()
endif()

add_executable(etunnel etunnel.cpp)
if(ASIO_DEFINITIONS)
  target_compile_definitions(etunnel PRIVATE ${ASIO_DEFINITIONS})
endif()
target_include_directories(etunnel PRIVATE ${WEBSOCKETPP_INCLUDE_DIR} ${ASIO_INCLUDE_DIR})
target_link_libraries(etunnel PRIVATE CURL::libcurl OpenSSL::SSL OpenSSL::Crypto Threads::Threads)
